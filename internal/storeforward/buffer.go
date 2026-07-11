// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package storeforward

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	pb "nexus-gateway/gen"
)

// StoredFrame pairs a sequence number with a TelemetryFrame read from the buffer.
type StoredFrame struct {
	Seq   int64
	Frame *pb.TelemetryFrame
}

// Buffer is a bounded SQLite ring buffer (ADR-0002).
// On overflow it drops the oldest rows. Cursor tracks the last acked seq.
type Buffer struct {
	db       *sql.DB
	capacity int

	mu     sync.Mutex
	drifts map[string]int64

	// Store-and-forward observability counters (ADR-0002). Atomic: written from
	// the pump goroutine (written/dropped) and the uplink Forwarder goroutine
	// (sent/checkpoints/sendErrors), read from the Admin API handler goroutine.
	written     atomic.Int64
	dropped     atomic.Int64
	sent        atomic.Int64
	accepted    atomic.Int64
	checkpoints atomic.Int64
	sendErrors  atomic.Int64

	// driftTotal is the running sum of per-point drift (accepted<sent shortfall,
	// ADR-0002), so designed loss is exposable as a single counter without summing
	// the per-point map on every scrape (#24). lastCheckpointUnix is the wall clock
	// of the most recent successful ack-checkpoint, feeding a staleness metric (#23).
	driftTotal         atomic.Int64
	lastCheckpointUnix atomic.Int64

	// notify is signaled (non-blocking, coalesced) after each successful Write so
	// the single uplink Forwarder can drain immediately instead of polling (#71).
	notify chan struct{}
}

// Open opens (or creates) a Buffer at the given file path with the given capacity.
//
// The pump (Write) and the uplink Forwarder (Advance) both write, so they
// contend for SQLite's single writer. To avoid SQLITE_BUSY 'database is locked'
// under high write rates (#109) we cap the pool at a single connection so all
// access is serialized at the Go layer instead of racing across connections,
// and set WAL + synchronous=NORMAL + busy_timeout on that connection (the
// timeout is a hedge in case the cap is ever raised). Pragmas are applied via
// Exec rather than a `file:` DSN so the raw path is honored verbatim (paths
// containing URI metacharacters like '#', '?', '%', or ':memory:' would be
// misparsed as a URI).
func Open(path string, capacity int) (*Buffer, error) {
	// A non-positive capacity makes every Write insert-then-evict in the same
	// transaction — 100% silent telemetry loss with a green process (#26).
	// Reject it here so a misconfiguration fails fast instead of dropping data.
	if capacity <= 0 {
		return nil, fmt.Errorf("storeforward: capacity must be positive, got %d", capacity)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// One connection: the pump and Forwarder queue on it rather than colliding
	// on the SQLite writer lock. Set before any Exec so the pragmas below land
	// on the single connection the pool will reuse.
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA busy_timeout=5000`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close() //nolint:errcheck
			return nil, fmt.Errorf("sqlite %s: %w", pragma, err)
		}
	}
	if err := migrate(db); err != nil {
		db.Close() //nolint:errcheck
		return nil, err
	}
	b := &Buffer{db: db, capacity: capacity, drifts: make(map[string]int64), notify: make(chan struct{}, 1)}
	// Seed the checkpoint clock to open time so the staleness metric measures
	// "seconds since boot" until the first real checkpoint, instead of reporting
	// epoch 0 (1970) and firing a spurious staleness alert on a fresh gateway that
	// has a backlog but has not yet completed its first ack-checkpoint (#23).
	b.lastCheckpointUnix.Store(time.Now().Unix())
	return b, nil
}

// WriteNotify returns a channel signaled (coalesced to one pending slot) after
// each successful Write. The single uplink consumer selects on it to drain
// promptly; missed signals are covered by the consumer's own backstop tick.
func (b *Buffer) WriteNotify() <-chan struct{} { return b.notify }

// Close closes the underlying database.
func (b *Buffer) Close() error {
	return b.db.Close()
}

// Write appends a frame. If the buffer is at capacity, the oldest row is deleted first.
func (b *Buffer) Write(f *pb.TelemetryFrame) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.Exec(
		`INSERT INTO frames (gateway_id, point_id, value, timestamp) VALUES (?, ?, ?, ?)`,
		f.GatewayId, f.PointId, f.Value, f.Timestamp,
	)
	if err != nil {
		return err
	}

	// Drop oldest if over capacity
	res, err := tx.Exec(`
		DELETE FROM frames
		WHERE seq IN (
			SELECT seq FROM frames ORDER BY seq ASC LIMIT MAX(0, (SELECT COUNT(*) FROM frames) - ?)
		)`, b.capacity)
	if err != nil {
		return err
	}
	evicted, _ := res.RowsAffected()

	if err := tx.Commit(); err != nil {
		return err
	}
	b.written.Add(1)
	if evicted > 0 {
		b.dropped.Add(evicted)
	}
	if b.notify != nil {
		select {
		case b.notify <- struct{}{}:
		default: // a signal is already pending; coalesce
		}
	}
	return nil
}

// RecordSent adds n to the count of frames acked-as-sent to Building OS.
func (b *Buffer) RecordSent(n int64) { b.sent.Add(n) }

// RecordAccepted adds n to the count of frames Building OS acknowledged as
// accepted (the cumulative StreamAck delta for a checkpoint). accepted <= sent;
// the shortfall is drift (#47 pipeline figures).
func (b *Buffer) RecordAccepted(n int64) { b.accepted.Add(n) }

// RecordCheckpoint counts one successful ack-checkpoint (ADR-0002) and stamps the
// checkpoint clock used by the staleness metric (#23).
func (b *Buffer) RecordCheckpoint() {
	b.checkpoints.Add(1)
	b.lastCheckpointUnix.Store(time.Now().Unix())
}

// RecordSendError counts one uplink send/checkpoint failure.
func (b *Buffer) RecordSendError() { b.sendErrors.Add(1) }

// Written returns the total frames successfully written to the buffer.
func (b *Buffer) Written() int64 { return b.written.Load() }

// Dropped returns the total frames evicted by drop-oldest at capacity (ADR-0002).
func (b *Buffer) Dropped() int64 { return b.dropped.Load() }

// Sent returns the total frames acked-as-sent to Building OS.
func (b *Buffer) Sent() int64 { return b.sent.Load() }

// Accepted returns the total frames Building OS acknowledged as accepted.
func (b *Buffer) Accepted() int64 { return b.accepted.Load() }

// Checkpoints returns the total successful ack-checkpoints.
func (b *Buffer) Checkpoints() int64 { return b.checkpoints.Load() }

// SendErrors returns the total uplink send/checkpoint failures.
func (b *Buffer) SendErrors() int64 { return b.sendErrors.Load() }

// DriftTotal returns the running sum of per-point drift (frames Building OS
// rejected under best-effort semantics, ADR-0002).
func (b *Buffer) DriftTotal() int64 { return b.driftTotal.Load() }

// LastCheckpointUnix returns the wall-clock unix time of the most recent
// successful ack-checkpoint, or 0 if none has occurred yet.
func (b *Buffer) LastCheckpointUnix() int64 { return b.lastCheckpointUnix.Load() }

// ReadBatch returns up to limit frames with seq > afterSeq, in ascending order.
func (b *Buffer) ReadBatch(afterSeq int64, limit int) ([]StoredFrame, error) {
	rows, err := b.db.Query(
		`SELECT seq, gateway_id, point_id, value, timestamp FROM frames WHERE seq > ? ORDER BY seq ASC LIMIT ?`,
		afterSeq, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var batch []StoredFrame
	for rows.Next() {
		var sf StoredFrame
		sf.Frame = &pb.TelemetryFrame{}
		if err := rows.Scan(&sf.Seq, &sf.Frame.GatewayId, &sf.Frame.PointId, &sf.Frame.Value, &sf.Frame.Timestamp); err != nil {
			return nil, err
		}
		batch = append(batch, sf)
	}
	return batch, rows.Err()
}

// Advance persists the cursor to seq. Future ReadBatch calls with afterSeq=cursor skip delivered frames.
func (b *Buffer) Advance(seq int64) error {
	_, err := b.db.Exec(`INSERT OR REPLACE INTO cursor (id, seq) VALUES (1, ?)`, seq)
	return err
}

// Cursor returns the current persisted cursor (last acked seq).
// Returns 0 for a fresh buffer (no cursor row yet). Logs a warning for any other error.
func (b *Buffer) Cursor() int64 {
	var seq int64
	err := b.db.QueryRow(`SELECT seq FROM cursor WHERE id = 1`).Scan(&seq)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("storeforward: cursor read error", "err", err)
	}
	return seq
}

// Depth returns the un-forwarded backlog: frames with seq beyond the cursor.
// Rows are retained after ack (only dropped on capacity overflow), so a plain
// COUNT(*) would track written_total rather than the real send backlog (#109).
func (b *Buffer) Depth() int64 {
	var n int64
	if err := b.db.QueryRow(
		`SELECT COUNT(*) FROM frames WHERE seq > COALESCE((SELECT seq FROM cursor WHERE id = 1), 0)`,
	).Scan(&n); err != nil {
		slog.Warn("storeforward: depth query error", "err", err)
	}
	return n
}

// RecordDrift increments the in-memory drift counter for pointID by delta, and
// the running total for the aggregate counter series (#24).
func (b *Buffer) RecordDrift(pointID string, delta int64) {
	b.mu.Lock()
	b.drifts[pointID] += delta
	b.mu.Unlock()
	b.driftTotal.Add(delta)
}

// Drifts returns a snapshot of per-point_id drift counters.
func (b *Buffer) Drifts() map[string]int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]int64, len(b.drifts))
	for k, v := range b.drifts {
		out[k] = v
	}
	return out
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS frames (
			seq        INTEGER PRIMARY KEY AUTOINCREMENT,
			gateway_id TEXT NOT NULL DEFAULT '',
			point_id   TEXT NOT NULL,
			value      REAL NOT NULL,
			timestamp  TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS cursor (
			id  INTEGER PRIMARY KEY CHECK (id = 1),
			seq INTEGER NOT NULL DEFAULT 0
		);
	`)
	return err
}
