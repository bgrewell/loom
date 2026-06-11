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
	if !s.Pace(context.Background()) {
		t.Fatal("soak should always pace with a live context")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s.Pace(ctx) {
		t.Fatal("soak should stop on a cancelled context")
	}
}

func TestIntervalPace(t *testing.T) {
	i := NewInterval(time.Millisecond)
	if i.Name() != "interval" {
		t.Fatalf("name = %q, want interval", i.Name())
	}
	// First pace is due immediately.
	if !i.Pace(context.Background()) {
		t.Fatal("first interval pace should fire immediately")
	}
	// A subsequent pace with a cancelled context must stop, not block.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if i.Pace(ctx) {
		t.Fatal("interval should stop on a cancelled context")
	}
}

func TestIntervalReleasesOverTime(t *testing.T) {
	i := NewInterval(2 * time.Millisecond)
	ctx := context.Background()
	start := time.Now()
	const n = 5
	for k := 0; k < n; k++ {
		if !i.Pace(ctx) {
			t.Fatalf("pace %d returned false", k)
		}
	}
	if elapsed := time.Since(start); elapsed < 6*time.Millisecond {
		t.Fatalf("releasing %d packets took %v, expected the interval to pace it", n, elapsed)
	}
}
