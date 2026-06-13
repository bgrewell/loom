// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package pump

import (
	"context"
	"testing"

	"github.com/bgrewell/loom/core/accounting"
	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/generator"
	"github.com/bgrewell/loom/core/payload"
	"github.com/bgrewell/loom/core/scheduler"
)

// nGen yields a fixed-size packet n times, then reports done.
type nGen struct{ left int }

func (*nGen) Name() string { return "n" }
func (g *nGen) Next(buf []byte) (int, bool) {
	if g.left == 0 {
		return 0, true
	}
	g.left--
	n := copy(buf, []byte("data"))
	return n, g.left == 0
}

func TestPumpRunsAndAccounts(t *testing.T) {
	var acct accounting.Counters
	p := New(&nGen{left: 5}, scheduler.Soak{}, datapath.Discard{}, &acct, 1500)
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := acct.Packets(); got != 5 {
		t.Fatalf("packets = %d, want 5", got)
	}
	if got := acct.Bytes(); got != 20 { // 5 packets × 4 bytes
		t.Fatalf("bytes = %d, want 20", got)
	}
}

func TestPumpStopsOnContext(t *testing.T) {
	var acct accounting.Counters
	// Soak generator that never finishes on its own.
	gen := generator.NewStream(payload.NewRandom(1500, 1), 1400)
	p := New(gen, scheduler.Soak{}, datapath.Discard{}, &acct, 1500)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Run(ctx); err != nil {
		t.Fatalf("Run on cancelled ctx: %v", err)
	}
}

func TestPumpStepAllocFree(t *testing.T) {
	gen := generator.NewStream(payload.NewRandom(1500, 1), 1400)
	var acct accounting.Counters
	dp := datapath.Discard{}
	buf := make([]byte, 1500)

	allocs := testing.AllocsPerRun(1000, func() {
		n, _ := gen.Next(buf)
		m, _ := dp.Send(buf[:n])
		acct.Add(uint64(m))
	})
	if allocs != 0 {
		t.Fatalf("pump step allocations = %v, want 0", allocs)
	}
}
