// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package storeforward

import "testing"

// The window is bounded: once full, adding evicts the oldest sequence.
func TestSeqWindow_EvictsOldest(t *testing.T) {
	w := newSeqWindow(3)
	for _, s := range []uint64{1, 2, 3} {
		w.add(s)
	}
	for _, s := range []uint64{1, 2, 3} {
		if !w.contains(s) {
			t.Fatalf("seq %d should be in the window", s)
		}
	}

	w.add(4) // evicts 1
	if w.contains(1) {
		t.Fatal("seq 1 should have been evicted")
	}
	for _, s := range []uint64{2, 3, 4} {
		if !w.contains(s) {
			t.Fatalf("seq %d should still be in the window", s)
		}
	}
	if len(w.seen) != 3 {
		t.Fatalf("window holds %d entries, want 3", len(w.seen))
	}
}
