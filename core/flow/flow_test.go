// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package flow

import (
	"context"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/generator"
	"github.com/bgrewell/loom/core/payload"
	"github.com/bgrewell/loom/core/scheduler"
)

func newStream(size int) generator.Generator {
	return generator.NewStream(payload.NewRandom(2048, 1), size)
}

func TestFlowStopCount(t *testing.T) {
	f := &Flow{
		Generator: newStream(100),
		Scheduler: scheduler.Soak{},
		Datapath:  datapath.Discard{},
		Stop:      Stop{Count: 100},
	}
	if err := f.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := f.Counters().Packets(); got != 100 {
		t.Fatalf("packets = %d, want 100", got)
	}
}

func TestFlowStopVolume(t *testing.T) {
	f := &Flow{
		Generator: newStream(100),
		Scheduler: scheduler.Soak{},
		Datapath:  datapath.Discard{},
		Stop:      Stop{Volume: 1000},
	}
	if err := f.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Stops at the first packet that reaches/exceeds 1000 bytes; with 100-byte
	// packets that's exactly 1000 (overshoot at most one packet).
	b := f.Counters().Bytes()
	if b < 1000 || b >= 1100 {
		t.Fatalf("bytes = %d, want in [1000,1100)", b)
	}
}

func TestFlowStopAfter(t *testing.T) {
	f := &Flow{
		Generator: newStream(1400),
		Scheduler: scheduler.Soak{},
		Datapath:  datapath.Discard{},
		Stop:      Stop{After: 20 * time.Millisecond},
	}
	start := time.Now()
	if err := f.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("duration-bounded flow ran too long: %v", elapsed)
	}
	if f.Counters().Packets() == 0 {
		t.Fatal("expected some packets to be sent")
	}
}

func TestFlowUntilStopped(t *testing.T) {
	f := &Flow{
		Generator: newStream(1400),
		Scheduler: scheduler.Soak{},
		Datapath:  datapath.Discard{},
		// zero Stop → runs until ctx is cancelled
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := f.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.Counters().Packets() == 0 {
		t.Fatal("expected some packets before cancellation")
	}
}
