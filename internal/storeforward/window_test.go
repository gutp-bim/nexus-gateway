// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package storeforward

import (
	"math/rand"
	"testing"
)

func TestSeqWindow_RecordsAndRecognises(t *testing.T) {
	w := newSeqWindow(64)
	for _, s := range []uint64{1, 5, 40, 63} {
		if w.contains(s) {
			t.Fatalf("seq %d must start unseen", s)
		}
		w.add(s)
		if !w.contains(s) {
			t.Fatalf("seq %d must be seen after add", s)
		}
	}
	if w.contains(2) {
		t.Fatal("seq 2 was never added")
	}
}

// Sliding forward forgets exactly the sequences that fall out of the window.
func TestSeqWindow_SlidesAndForgetsOldest(t *testing.T) {
	w := newSeqWindow(64) // covers 64 consecutive sequences
	w.add(1)
	w.add(10)
	w.add(64) // still inside [0, 64)? no: 64-0 >= 64, so the window slides to [1, 65)
	if !w.contains(1) || !w.contains(10) || !w.contains(64) {
		t.Fatal("1, 10 and 64 are all inside [1, 65)")
	}
	w.add(70) // slides to [7, 71): 1 falls out, 10 stays
	if w.contains(1) {
		t.Fatal("seq 1 fell out of the window and must read as unseen")
	}
	if !w.contains(10) || !w.contains(64) || !w.contains(70) {
		t.Fatal("10, 64, 70 must survive the slide")
	}
}

// A sequence older than the window cannot be judged: it reads as unseen and is not
// recorded (so it cannot corrupt a newer slot that shares its bit).
func TestSeqWindow_OlderThanWindowIsIgnored(t *testing.T) {
	w := newSeqWindow(64)
	w.add(1000)
	w.add(3) // far below base
	if w.contains(3) {
		t.Fatal("a sequence below base must read as unseen")
	}
	if !w.contains(1000) {
		t.Fatal("recording an old sequence must not disturb the newest one")
	}
	// 3 and 1003 share a bit position in a 64-wide window; 3 must not have set it.
	if w.contains(1003) {
		t.Fatal("seq 1003 was never added")
	}
}

// A jump of a whole window or more resets everything.
func TestSeqWindow_LargeJumpClearsAll(t *testing.T) {
	w := newSeqWindow(64)
	for s := uint64(1); s <= 50; s++ {
		w.add(s)
	}
	w.add(1 << 40)
	for s := uint64(1); s <= 50; s++ {
		if w.contains(s) {
			t.Fatalf("seq %d must be forgotten after a window-sized jump", s)
		}
	}
	if !w.contains(1 << 40) {
		t.Fatal("the newest sequence must be recorded")
	}
}

// The bitmap agrees with a naive model for any in-window pattern, including
// out-of-order arrivals (redeliveries) and slides.
func TestSeqWindow_MatchesNaiveModel(t *testing.T) {
	const span = 256
	rng := rand.New(rand.NewSource(1))
	w := newSeqWindow(span)
	model := map[uint64]bool{}
	var newest uint64
	for range 20000 {
		// mostly advancing, sometimes a redelivery from the recent past
		var s uint64
		if rng.Intn(4) == 0 && newest > 10 {
			s = newest - uint64(rng.Intn(10))
		} else {
			newest += uint64(1 + rng.Intn(3))
			s = newest
		}
		base := uint64(0)
		if newest >= span {
			base = newest - span + 1
		}
		if s >= base { // in the window the model is exact
			if got, want := w.contains(s), model[s]; got != want {
				t.Fatalf("contains(%d) = %v, model says %v", s, got, want)
			}
		}
		w.add(s)
		model[s] = true
	}
}
