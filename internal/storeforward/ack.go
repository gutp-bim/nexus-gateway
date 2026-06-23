// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package storeforward

// SentFrame records a frame that was transmitted upstream in the current batch.
// Used by ApplyAck to compute drift without carrying a full TelemetryFrame.
type SentFrame struct {
	Seq     int64
	PointID string
}

// ApplyAck processes an acknowledgment for a sent batch (ADR-0002).
//
// Returns:
//   - newCursor: seq of the last frame in batch; advance the S&F cursor to this value
//     regardless of how many were accepted (never replay stale frames).
//   - drifts: per-point-ID count of frames that were NOT accepted. Callers should add
//     these to the buffer's drift counters via RecordDrift.
//
// A negative accepted value is clamped to 0 (guard against buggy server responses).
func ApplyAck(batch []SentFrame, accepted int64) (newCursor int64, drifts map[string]int64) {
	if len(batch) == 0 {
		return 0, nil
	}
	newCursor = batch[len(batch)-1].Seq

	lost := int64(len(batch)) - accepted
	if lost <= 0 {
		return newCursor, nil
	}

	start := int(accepted)
	if start < 0 {
		start = 0
	}

	drifts = make(map[string]int64)
	for _, sf := range batch[start:] {
		drifts[sf.PointID]++
	}
	return newCursor, drifts
}
