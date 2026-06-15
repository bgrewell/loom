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

// TestIntervalReleasesBurst verifies the scheduler returns every packet due since
// the last call (capped at max), so the data plane batches instead of paying a
// per-packet timer. Deterministic via an injected clock.
func TestIntervalReleasesBurst(t *testing.T) {
	i := NewInterval(10 * time.Millisecond)
	base := time.Unix(100, 0)
	cur := base
	i.now = func() time.Time { return cur }
	ctx := context.Background()

	i.Pace(ctx, 64) // baseline: releases 1, next = base+10ms

	// 50 ms later, 5 packets are due (at base+10,20,30,40,50ms).
	cur = base.Add(50 * time.Millisecond)
	if n, ok := i.Pace(ctx, 64); !ok || n != 5 {
		t.Fatalf("Pace = (%d, %v), want 5 packets due", n, ok)
	}
	// Next = base+60ms. At base+90ms four packets are due (60,70,80,90ms) but
	// max caps the burst at 3.
	cur = base.Add(90 * time.Millisecond)
	if n, _ := i.Pace(ctx, 3); n != 3 {
		t.Fatalf("Pace capped = %d, want 3 (max)", n)
	}
}

// TestIntervalAchievesRate is the regression guard for #73: with a microsecond
// gap, the old per-packet timer capped throughput ~10x below target; the burst +
// sleep-coarse-then-spin path should track it. Real-time, so the band is wide.
func TestIntervalAchievesRate(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive")
	}
	const gap = 10 * time.Microsecond // 100k packets/sec target
	i := NewInterval(gap)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	sent := 0
	for time.Since(start) < 200*time.Millisecond {
		n, ok := i.Pace(ctx, 64)
		if !ok {
			t.Fatal("Pace returned false")
		}
		sent += n
	}
	pps := float64(sent) / time.Since(start).Seconds()
	if pps < 70_000 || pps > 130_000 {
		t.Errorf("achieved %.0f pps, want ~100000 (±30%%) — per-packet-timer regression?", pps)
	}
}
