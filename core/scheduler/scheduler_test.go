// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"context"
	"testing"
	"time"
)

func TestSoakPace(t *testing.T) {
	var s Soak
	if s.Name() != "soak" {
		t.Fatalf("name = %q, want soak", s.Name())
	}
	if n, ok := s.Pace(context.Background(), 64); !ok || n != 64 {
		t.Fatalf("soak Pace(_,64) = (%d,%v), want (64,true) for batching", n, ok)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := s.Pace(ctx, 64); ok {
		t.Fatal("soak should stop on a cancelled context")
	}
}

func TestIntervalPace(t *testing.T) {
	i := NewInterval(time.Millisecond)
	if i.Name() != "interval" {
		t.Fatalf("name = %q, want interval", i.Name())
	}
	// First pace is due immediately and releases exactly one (strict pacing).
	if n, ok := i.Pace(context.Background(), 64); !ok || n != 1 {
		t.Fatalf("first interval Pace = (%d,%v), want (1,true)", n, ok)
	}
	// A subsequent pace with a cancelled context must stop, not block.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := i.Pace(ctx, 64); ok {
		t.Fatal("interval should stop on a cancelled context")
	}
}

func TestIntervalReleasesOverTime(t *testing.T) {
	i := NewInterval(2 * time.Millisecond)
	ctx := context.Background()
	start := time.Now()
	const n = 5
	for k := 0; k < n; k++ {
		if got, ok := i.Pace(ctx, 64); !ok || got != 1 {
			t.Fatalf("pace %d = (%d,%v), want (1,true)", k, got, ok)
		}
	}
	if elapsed := time.Since(start); elapsed < 6*time.Millisecond {
		t.Fatalf("releasing %d packets took %v, expected the interval to pace it", n, elapsed)
	}
}

// TestIntervalBatchesWhenDue covers the catch-up path: when several send times
// have already passed, Pace releases them in one batch (capped at max).
func TestIntervalBatchesWhenDue(t *testing.T) {
	base := time.Unix(100, 0)
	cur := base
	i := NewInterval(100 * time.Microsecond)
	i.now = func() time.Time { return cur }
	if n, ok := i.Pace(context.Background(), 64); !ok || n != 1 {
		t.Fatalf("first pace = (%d,%v), want (1,true)", n, ok)
	}
	cur = base.Add(50 * 100 * time.Microsecond) // 50 packets now due
	n, ok := i.Pace(context.Background(), 64)
	if !ok || n < 50 {
		t.Fatalf("catch-up pace = (%d,%v), want >=50", n, ok)
	}
}

// TestIntervalReleasesBurstsNotPerPacket guards against the busy-spin
// regression: at a high rate a fast caller must get more than one packet per
// wake (the scheduler sleeps between bursts), not one packet per wake (which
// made it spin a whole core per flow).
func TestIntervalReleasesBurstsNotPerPacket(t *testing.T) {
	i := NewInterval(200 * time.Microsecond) // 5k pps; gap far below the spin window
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	maxBatch := 0
	for {
		n, ok := i.Pace(ctx, 64)
		if !ok {
			break
		}
		if n > maxBatch {
			maxBatch = n
		}
	}
	if maxBatch < 2 {
		t.Fatalf("largest batch per wake = %d, want >= 2 (pacer is waking per packet)", maxBatch)
	}
}
