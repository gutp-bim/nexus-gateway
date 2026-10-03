// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package normalizer

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	pb "nexus-gateway/gen"
	"nexus-gateway/internal/common"
	"nexus-gateway/internal/metrics"
	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/storeforward"
)

// Outcome classifies a Common Event so the consume loop can drop-and-meter
// poison and point-list-miss events distinctly (ADR-0002 best-effort).
type Outcome int

const (
	OutcomeOK     Outcome = iota // resolved → emit a TelemetryFrame
	OutcomePoison                // unparseable/permanently invalid → Term + meter
	OutcomeMiss                  // unknown local_id → Term + meter
)

// EventMsg is one fetched Common Event with its ack controls. It is the subset
// of jetstream.Msg the consume loop needs (which therefore satisfies it directly).
type EventMsg interface {
	Data() []byte
	// Metadata carries the stream sequence (the Pump's idempotency key, #186) and
	// the delivery count (to meter redeliveries).
	Metadata() (*jetstream.MsgMetadata, error)
	Ack() error
	Term() error
	Nak() error
	// NakWithDelay redelivers after d; the Pump uses it to back off a frame whose
	// durable buffer write failed (#28). jetstream.Msg satisfies it directly.
	NakWithDelay(d time.Duration) error
}

// EventSource is the seam over the durable JetStream pull consumer: it yields the
// next batch of Common Events as an iterator. Yielding (rather than returning a
// slice) preserves streaming — each message is processed as it arrives, not after
// the whole batch closes — which keeps ack/term latency low. JetStream is one
// adapter (jetstreamSource); tests inject an in-memory fake, so the consume loop
// is exercisable without NATS.
type EventSource interface {
	Fetch(max int, maxWait time.Duration) iter.Seq[EventMsg]
}

const (
	// fetchBatch is the most messages pulled per Fetch.
	fetchBatch = 32

	// ackWait is the consumer's ack deadline. A message is acked only after the
	// Pump's durable write, so it can legitimately wait behind the hand-off queues
	// while the pipeline is busy; the JetStream default (30 s) let such a message
	// expire and be redelivered under load (#186).
	ackWait = 2 * time.Minute
)

// Normalizer is the single durable pull consumer on evt.> (ADR-0001, ADR-0005).
// It resolves native LocalID → canonical PointID via the resolver, then emits
// TelemetryFrames downstream. Unknown local_ids are skipped and metered.
type Normalizer struct {
	frames chan storeforward.FrameMsg
}

// New wires the Normalizer to the live JetStream EVENTS stream (ADR-0005),
// creating the durable consumer and feeding the consume loop through a
// jetstreamSource adapter.
func New(ctx context.Context, js jetstream.JetStream, resolver pointlist.Resolver, gatewayID string) (*Normalizer, error) {
	cons, err := js.CreateOrUpdateConsumer(ctx, "EVENTS", jetstream.ConsumerConfig{
		Durable:       "normalizer",
		FilterSubject: "evt.>",
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       ackWait,
		MaxDeliver:    3,
	})
	if err != nil {
		return nil, fmt.Errorf("create normalizer consumer: %w", err)
	}
	return NewWithSource(ctx, jetstreamSource{cons: cons}, resolver, gatewayID), nil
}

// NewWithSource starts a Normalizer over an arbitrary EventSource. This is the
// testable seam; New is the production wrapper over JetStream.
func NewWithSource(ctx context.Context, src EventSource, resolver pointlist.Resolver, gatewayID string) *Normalizer {
	n := &Normalizer{frames: make(chan storeforward.FrameMsg, 256)}
	go n.consume(ctx, src, resolver, gatewayID)
	return n
}

// Frames returns the channel of normalized TelemetryFrames, each paired with its
// source message so the downstream Pump acks only after a durable write (#28).
func (n *Normalizer) Frames() <-chan storeforward.FrameMsg {
	return n.frames
}

func (n *Normalizer) consume(ctx context.Context, src EventSource, resolver pointlist.Resolver, gatewayID string) {
	defer close(n.frames)
	for {
		if ctx.Err() != nil {
			return
		}
		// Pull no more than the hand-off channel has room for, so a fetched message
		// never sits in hand waiting on a full channel while its ack deadline runs
		// (#186). Only this goroutine sends, so free space can only grow meanwhile.
		free := cap(n.frames) - len(n.frames)
		if free == 0 {
			select {
			case <-time.After(10 * time.Millisecond):
			case <-ctx.Done():
				return
			}
			continue
		}
		for msg := range src.Fetch(min(fetchBatch, free), 500*time.Millisecond) {
			var seq uint64
			if md, err := msg.Metadata(); err == nil {
				seq = md.Sequence.Stream
				if md.NumDelivered > 1 {
					metrics.IncNormalizerRedelivered()
				}
			}
			frame, out := Normalize(msg.Data(), resolver, gatewayID)
			switch out {
			case OutcomePoison:
				// Retrying an unparseable event is pointless; terminate, don't redeliver.
				metrics.IncNormalizerInvalid()
				_ = msg.Term()
				continue
			case OutcomeMiss:
				// The Point List is synced before telemetry flows (ADR-0003), so an
				// unknown local_id is misconfiguration, not a sync race: drop and meter.
				metrics.IncNormalizerUnresolved()
				_ = msg.Term()
				continue
			}
			// Hand the frame downstream WITH its source msg; the Pump acks only
			// after a durable buffer write (#28). Do not ack here — a write failure
			// after an enqueue-time ack would silently lose an already-acked frame.
			select {
			case n.frames <- storeforward.FrameMsg{Frame: frame, Msg: msg, Seq: seq}:
			case <-ctx.Done():
				_ = msg.Nak()
				return
			}
		}
	}
}

// jetstreamSource adapts a JetStream pull consumer to EventSource. It yields each
// fetched message as it streams off the batch; jetstream.Msg satisfies EventMsg.
type jetstreamSource struct {
	cons jetstream.Consumer
}

func (s jetstreamSource) Fetch(max int, maxWait time.Duration) iter.Seq[EventMsg] {
	return func(yield func(EventMsg) bool) {
		batch, err := s.cons.Fetch(max, jetstream.FetchMaxWait(maxWait))
		if err != nil {
			slog.Warn("normalizer: fetch error", "err", err)
			return
		}
		for m := range batch.Messages() {
			if !yield(m) {
				return
			}
		}
		if err := batch.Error(); err != nil {
			slog.Warn("normalizer: batch error", "err", err)
		}
	}
}

// Normalize maps a raw Common Event payload to a TelemetryFrame.
// It is a pure function: no I/O, no state. The consume loop calls it and
// acts on the returned Outcome (ack, term, or nak).
func Normalize(data []byte, resolver pointlist.Resolver, gatewayID string) (*pb.TelemetryFrame, Outcome) {
	var evt common.Event
	if err := json.Unmarshal(data, &evt); err != nil {
		slog.Warn("normalizer: unmarshal error", "err", err)
		return nil, OutcomePoison
	}
	pointID, ok := resolver.Resolve(evt.ConnectorID, evt.LocalID)
	if !ok {
		slog.Warn("normalizer: unknown local_id", "connector", evt.ConnectorID, "local_id", evt.LocalID)
		return nil, OutcomeMiss
	}
	ts := evt.Timestamp
	if ts == "" {
		ts = time.Now().UTC().Format(time.RFC3339)
	}
	// Unit and quality unification: ride in attributes (additive, merged into
	// the validated telemetry data object downstream). "Good" is the implied
	// default quality, so only deviations (Bad/Uncertain) are carried — this
	// keeps the steady-state frame lean.
	var attrs map[string]string
	if len(evt.Attributes) > 0 || evt.Unit != "" || (evt.Quality != "" && evt.Quality != "Good") {
		attrs = make(map[string]string, len(evt.Attributes)+2)
		for key, value := range evt.Attributes {
			attrs[key] = value
		}
		if evt.Unit != "" {
			attrs["unit"] = evt.Unit
		}
		if evt.Quality != "" && evt.Quality != "Good" {
			attrs["quality"] = evt.Quality
		}
	}
	frame := &pb.TelemetryFrame{
		GatewayId:  gatewayID,
		PointId:    pointID,
		Timestamp:  ts,
		Attributes: attrs,
	}
	switch evt.Value.Kind() {
	case common.ValueNumber:
		value, _ := evt.Value.Number()
		frame.Value = &pb.TelemetryFrame_ValueNum{ValueNum: value}
	case common.ValueString:
		value, _ := evt.Value.String()
		frame.Value = &pb.TelemetryFrame_ValueStr{ValueStr: value}
	case common.ValueBool:
		value, _ := evt.Value.Bool()
		frame.Value = &pb.TelemetryFrame_ValueBool{ValueBool: value}
	default:
		return nil, OutcomePoison
	}
	return frame, OutcomeOK
}
