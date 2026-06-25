# Extending the Northbound uplink: implementing `FrameSink`

Version: 1.0 (nexus-gateway v0.x)  
Status: Normative

This document explains how to add a new Northbound destination — something other
than the Building OS gRPC stream — by implementing the `uplink.FrameSink`
interface.  Building OS over gRPC (`grpcSink`) is the reference adapter; this
guide shows how to write your own.

---

## Contents

1. [Architecture context](#1-architecture-context)
2. [The `FrameSink` interface](#2-the-framesink-interface)
3. [Delivery contract you must honour](#3-delivery-contract-you-must-honour)
4. [Reference implementation: `grpcSink`](#4-reference-implementation-grpcsink)
5. [Step-by-step: writing a new Sink](#5-step-by-step-writing-a-new-sink)
6. [Fan-out: delivering to multiple destinations](#6-fan-out-delivering-to-multiple-destinations)
7. [Testing your Sink](#7-testing-your-sink)
8. [Wiring into `cmd/gateway/main.go`](#8-wiring-into-cmdgatewaymaingg)

---

## 1. Architecture context

Normalised `TelemetryFrame`s flow through the pipeline as follows:

```
Connectors
    │  Common Events (NATS JetStream)
    ▼
Normalizer
    │  TelemetryFrame
    ▼
storeforward.Buffer  (SQLite ring buffer, ADR-0002)
    │
    ▼
uplink.Forwarder
    │  FrameSink.Send / FrameSink.Checkpoint
    ▼
[ your Sink here ]   ← extension point
    │
    ▼
Northbound destination
(Building OS gRPC, MQTT broker, REST endpoint, …)
```

`uplink.Forwarder` owns the best-effort store-and-forward delivery policy
(ADR-0002).  It reads frames from the `storeforward.Buffer` and delegates
*transport* entirely to whichever `FrameSink` is injected.  The policy —
checkpoint cadence, cursor advance, drift recording — is identical regardless of
the Sink; only the on-the-wire encoding changes.

---

## 2. The `FrameSink` interface

```go
// package uplink

type FrameSink interface {
    Send(ctx context.Context, frame *pb.TelemetryFrame) error
    Checkpoint(ctx context.Context) (accepted int64, err error)
}
```

`pb.TelemetryFrame` is defined in `gen/` (generated from `proto/gateway.proto`):

```proto
message TelemetryFrame {
    string gateway_id  = 1;
    string point_id    = 2;
    double value       = 3;
    string timestamp   = 4;   // RFC 3339 UTC
    map<string,string> attributes = 5;
}
```

| Method | Called by Forwarder | Purpose |
|--------|---------------------|---------|
| `Send` | once per frame, immediately on arrival | Transmit the frame to the destination. |
| `Checkpoint` | every `CheckpointSize` frames or `CheckpointAge` | Flush and confirm how many frames the destination accepted. |

---

## 3. Delivery contract you must honour

### `Send`

- Must attempt to transmit the frame.
- Return `nil` on success (frame handed off to the transport layer).
- Return a non-`nil` error if the frame **cannot** be handed off (connection
  broken, buffer full, etc.).  The Forwarder records a send-error counter and
  stops the current forwarding session; the cursor is left un-advanced so the
  batch is replayed on reconnect.
- **Do not** silently drop the frame and return `nil` — that would advance the
  cursor past an undelivered frame and is indistinguishable from successful
  delivery (best-effort, ADR-0002).

### `Checkpoint`

- Flush any buffered data and determine how many frames the destination received.
- Return `accepted` = the total number of frames the destination has actually
  ingested since the previous `Checkpoint` (or since the Sink was last reset for
  a new session).
- If `accepted < len(batch)` the Forwarder records the shortfall as per-`point_id`
  drift; it still advances the cursor past the whole batch (no resend).
- Return an error if the destination is unreachable or the flush fails.  The
  Forwarder will end the session and reconnect.
- After a successful `Checkpoint` the Sink must be ready to start a fresh batch
  from zero — reset any internal sequence counters.

### Session lifecycle

The Forwarder creates a new `FrameSink` per session (see `Ingress.Run`).  On
session start `accepted` counts from 0.  On `Send` error or `Checkpoint` error,
the Forwarder discards the current Sink and starts a new session with a new Sink
instance.  Design your Sink so that a new instance safely starts fresh.

---

## 4. Reference implementation: `grpcSink`

`internal/uplink/ingress.go` contains the production adapter.  It is the
simplest possible implementation and a good starting point:

```go
type grpcSink struct {
    client pb.GatewayIngressClient
    stream pb.GatewayIngress_StreamTelemetryClient // nil until first Send
}

func (g *grpcSink) Send(ctx context.Context, frame *pb.TelemetryFrame) error {
    if g.stream == nil {
        stream, err := g.client.StreamTelemetry(ctx)
        if err != nil {
            return err
        }
        g.stream = stream
    }
    return g.stream.Send(frame)
}

func (g *grpcSink) Checkpoint(_ context.Context) (int64, error) {
    if g.stream == nil {
        return 0, nil
    }
    ack, err := g.stream.CloseAndRecv()
    g.stream = nil  // reset: next Send will open a fresh stream
    if err != nil {
        return 0, fmt.Errorf("checkpoint recv: %w", err)
    }
    return ack.Accepted, nil
}
```

Key patterns to notice:

- **Lazy open** — the gRPC stream is opened on the first `Send`, not in the
  constructor.  This avoids holding an idle connection that server-side
  idle-timeout policies would tear down before any frame arrives.
- **Half-close on Checkpoint** — `CloseAndRecv()` half-closes the client stream
  and waits for the server to reply with the accepted count, then sets `stream`
  back to `nil` so the next `Send` opens a fresh one.
- **No internal buffering** — each `Send` call transmits immediately.  Batching
  is the Forwarder's job via `CheckpointSize`/`CheckpointAge`.

---

## 5. Step-by-step: writing a new Sink

### Example: MQTT publisher

```go
// internal/uplink/mqttsink/mqttsink.go
package mqttsink

import (
    "context"
    "encoding/json"
    "fmt"
    "sync/atomic"

    mqtt "github.com/eclipse/paho.golang/paho"

    pb "nexus-gateway/gen"
)

// MQTTSink publishes TelemetryFrames as JSON to an MQTT broker.
// Each Send publishes one message; Checkpoint is a no-op that reports
// the count of successful publishes since the last reset.
type MQTTSink struct {
    client  *mqtt.Client
    topic   string
    sent    atomic.Int64
    failed  atomic.Int64
}

func New(client *mqtt.Client, topic string) *MQTTSink {
    return &MQTTSink{client: client, topic: topic}
}

func (s *MQTTSink) Send(ctx context.Context, frame *pb.TelemetryFrame) error {
    payload, err := json.Marshal(frame)
    if err != nil {
        return fmt.Errorf("marshal: %w", err)
    }
    _, err = s.client.Publish(ctx, &mqtt.Publish{
        Topic:   s.topic,
        QoS:     1,
        Payload: payload,
    })
    if err != nil {
        return fmt.Errorf("mqtt publish: %w", err)
    }
    s.sent.Add(1)
    return nil
}

// Checkpoint reports the number of frames published since the last reset.
// MQTT QoS 1 delivery is best-effort from this gateway's perspective,
// so accepted == sent (we never know if the broker forwarded onward).
func (s *MQTTSink) Checkpoint(_ context.Context) (int64, error) {
    n := s.sent.Swap(0)
    return n, nil
}
```

**When `accepted` should be less than `sent`**

If your destination provides a confirmation count (like Building OS
`StreamAck.accepted`), return that value.  If it doesn't (like MQTT QoS 1),
returning `sent` as `accepted` is correct — it tells the Forwarder all frames
were handed off and advances the cursor without drift.  Only return
`accepted < sent` if you have evidence that the destination rejected frames.

---

## 6. Fan-out: delivering to multiple destinations

To deliver each `TelemetryFrame` to more than one destination, wrap multiple
sinks behind a `fanOutSink`:

```go
// internal/uplink/fanout.go
package uplink

import (
    "context"
    "errors"

    pb "nexus-gateway/gen"
)

// FanOutSink delivers each frame to every inner Sink in order.
// Send fails fast on the first error. Checkpoint collects the minimum
// accepted count across all inner sinks (conservative best-effort).
type FanOutSink struct {
    sinks []FrameSink
}

func NewFanOutSink(sinks ...FrameSink) *FanOutSink {
    return &FanOutSink{sinks: sinks}
}

func (f *FanOutSink) Send(ctx context.Context, frame *pb.TelemetryFrame) error {
    for _, s := range f.sinks {
        if err := s.Send(ctx, frame); err != nil {
            return err
        }
    }
    return nil
}

func (f *FanOutSink) Checkpoint(ctx context.Context) (int64, error) {
    var minAccepted int64 = -1
    var errs []error
    for _, s := range f.sinks {
        accepted, err := s.Checkpoint(ctx)
        if err != nil {
            errs = append(errs, err)
            continue
        }
        if minAccepted < 0 || accepted < minAccepted {
            minAccepted = accepted
        }
    }
    if len(errs) > 0 {
        return 0, errors.Join(errs...)
    }
    if minAccepted < 0 {
        return 0, nil
    }
    return minAccepted, nil
}
```

Wire it in `main.go`:

```go
grpc := &grpcSink{client: bosClient}
mqtt := mqttsink.New(mqttClient, "telemetry/gateway/"+gatewayID)
sink := uplink.NewFanOutSink(grpc, mqtt)
fwd  := uplink.NewForwarder(buf, sink, uplink.DefaultConfig)
```

---

## 7. Testing your Sink

The `Forwarder` is already tested in `internal/uplink/forwarder_test.go` using
an in-memory `fakeSink`.  For your own Sink, test the `FrameSink` contract
directly without a live Forwarder:

```go
func TestMQTTSink_SendAndCheckpoint(t *testing.T) {
    broker := startTestBroker(t)   // test helper that starts a local broker
    client := connectTestClient(t, broker)
    sink   := mqttsink.New(client, "test/telemetry")

    frame := &pb.TelemetryFrame{
        GatewayId: "gw-1", PointId: "p1", Value: 42.0,
        Timestamp: "2026-01-01T00:00:00Z",
    }
    require.NoError(t, sink.Send(context.Background(), frame))

    accepted, err := sink.Checkpoint(context.Background())
    require.NoError(t, err)
    assert.Equal(t, int64(1), accepted)
}
```

To test the Forwarder with your Sink integrated, inject it directly:

```go
fwd := uplink.NewForwarder(buf, mySink, uplink.Config{CheckpointSize: 1, CheckpointAge: time.Hour})
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
go fwd.Run(ctx)
// assert frames arrive at your destination
```

---

## 8. Wiring into `cmd/gateway/main.go`

The relevant section of `cmd/gateway/main.go` (around line 227):

```go
// Before (Building OS gRPC only):
ul, err := uplink.NewIngress(ctx, *bosIngressAddr, *gatewayID, buf, uplink.DefaultConfig, bosCreds)
if err != nil { ... }
go ul.Run(ctx)
```

`uplink.Ingress` is a convenience wrapper that owns the gRPC reconnect loop and
creates a `grpcSink` per session.  To use a custom Sink you have two options:

**Option A — Replace `Ingress` with a custom reconnect loop**

```go
// Your reconnect loop, analogous to Ingress.Run:
go func() {
    bo := &retry.Backoff{Min: time.Second, Max: 60 * time.Second, Factor: 2.0}
    for ctx.Err() == nil {
        sink := mqttsink.New(mqttClient, "telemetry/"+*gatewayID)
        fwd  := uplink.NewForwarder(buf, sink, uplink.DefaultConfig)
        if err := fwd.Run(ctx); err != nil && ctx.Err() == nil {
            slog.Warn("mqtt sink error, reconnecting", "err", err)
            bo.Wait(ctx)
        } else {
            bo.Reset()
        }
    }
}()
```

**Option B — Keep `Ingress` and add a parallel Forwarder for the secondary Sink**

This requires a second `storeforward.Buffer` or a tee at the Normalizer output.
Fan-out at the `FrameSink` level (§6) is simpler and is the recommended approach
when both destinations share the same store-and-forward semantics.

---

## Summary of rules

| Rule | Must / Should / May |
|------|---------------------|
| Return error from `Send` if the frame cannot be handed off | **Must** |
| Return `accepted == sent` when the destination has no rejection signal | **Should** |
| Reset internal counters after each `Checkpoint` | **Must** |
| Open connections lazily (on first `Send`) | **Should** |
| Not buffer frames internally beyond what the transport requires | **Should** |
| Be safe to discard and replace between sessions | **Must** |
