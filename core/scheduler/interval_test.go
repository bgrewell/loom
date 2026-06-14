// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"context"
	"testing"
	"time"
)

// TestIntervalNoBurstAfterStall verifies that when the scheduler falls behind
// (a slow send / GC pause), it re-baselines to the current time instead of
// accumulating a backlog that would release as a rate-spiking burst. Uses an
// injected clock for determinism.
func TestIntervalNoBurstAfterStall(t *testing.T) {
	i := NewInterval(10 * time.Millisecond)
	base := time.Unix(100, 0)
	cur := base
	i.now = func() time.Time { return cur }
	ctx := context.Background()

	if _, ok := i.Pace(ctx, 1); !ok { // baseline; next = base+10ms
		t.Fatal("first Pace returned false")
	}
	// Simulate a 1s stall: we are now ~100 intervals behind.
	cur = base.Add(time.Second)
	if _, ok := i.Pace(ctx, 1); !ok {
		t.Fatal("Pace after stall returned false")
	}
	// Without re-baselining, next would be base+20ms (still ~980ms behind) and
	// the next ~98 Pace calls would fire immediately — a burst. With the fix,
	// next is re-based to cur+every.
	if got, want := i.next, cur.Add(10*time.Millisecond); got != want {
		t.Fatalf("next = %v, want %v (deficit should be dropped, not bursted)", got, want)
	}
}

// TestIntervalCancel: a cancelled context stops pacing.
func TestIntervalCancel(t *testing.T) {
	i := NewInterval(time.Hour) // long gap so Pace would block on the timer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := i.Pace(ctx, 1); ok {
		t.Fatal("Pace should return ok=false when ctx is cancelled")
	}
}
