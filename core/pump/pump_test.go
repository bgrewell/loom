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

// recordingTx is a TxDatapath that records the largest batch committed.
type recordingTx struct {
	frames   []datapath.Frame
	buf      [][]byte
	maxBatch int
}

func newRecordingTx(depth, fsize int) *recordingTx {
	r := &recordingTx{frames: make([]datapath.Frame, depth), buf: make([][]byte, depth)}
	for i := range r.buf {
		r.buf[i] = make([]byte, fsize)
	}
	return r
}

func (*recordingTx) Name() string                { return "rec" }
func (*recordingTx) Caps() datapath.Capabilities { return datapath.Capabilities{} }
func (*recordingTx) Close() error                { return nil }
func (r *recordingTx) TxReserve(n int) []datapath.Frame {
	if n > len(r.frames) {
		n = len(r.frames)
	}
	for i := 0; i < n; i++ {
		r.frames[i] = datapath.Frame{Data: r.buf[i]}
	}
	return r.frames[:n]
}
func (r *recordingTx) TxCommit(f []datapath.Frame) (int, error) {
	if len(f) > r.maxBatch {
		r.maxBatch = len(f)
	}
	return len(f), nil
}

// TestPumpBatchesUnderSoak: an unpaced flow commits packets in batches (not one
// at a time) while still accounting every packet exactly once.
func TestPumpBatchesUnderSoak(t *testing.T) {
	rec := newRecordingTx(64, 64)
	var acct accounting.Counters
	p := New(&nGen{left: 500}, scheduler.Soak{}, rec, &acct)
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.maxBatch <= 1 {
		t.Fatalf("soak should batch commits; maxBatch=%d", rec.maxBatch)
	}
	if got := acct.Packets(); got != 500 {
		t.Fatalf("packets = %d, want 500", got)
	}
}

// TestPumpPacedSendsOneAtATime: a rate scheduler keeps batches at 1 (strict
// pacing is not defeated by batching).
func TestPumpPacedSendsOneAtATime(t *testing.T) {
	rec := newRecordingTx(64, 64)
	var acct accounting.Counters
	sched := scheduler.NewInterval(0) // 0 gap: fires immediately but one at a time
	p := New(&nGen{left: 20}, sched, rec, &acct)
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.maxBatch != 1 {
		t.Fatalf("paced flow should commit one at a time; maxBatch=%d", rec.maxBatch)
	}
	if got := acct.Packets(); got != 20 {
		t.Fatalf("packets = %d, want 20", got)
	}
}

func TestPumpRunsAndAccounts(t *testing.T) {
	var acct accounting.Counters
	p := New(&nGen{left: 5}, scheduler.Soak{}, datapath.NewDiscard(1500), &acct)
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

// TestPumpRunsOverZeroCopyArena drives the pump over the native (non-adapter)
// zero-copy arena TxDatapath. The arena is sized to hold the whole run so the
// single-goroutine SPSC contract is respected (no concurrent drain).
func TestPumpRunsOverZeroCopyArena(t *testing.T) {
	var acct accounting.Counters
	arena := datapath.NewArena(8, 1500) // > the 4 packets we send
	p := New(&nGen{left: 4}, scheduler.Soak{}, arena, &acct)
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := acct.Packets(); got != 4 {
		t.Fatalf("packets = %d, want 4", got)
	}
	// The 4 transmitted packets are now receivable from the same slab (no copy).
	f, _ := arena.RxPoll(8)
	if len(f) != 4 {
		t.Fatalf("RxPoll returned %d frames, want 4", len(f))
	}
	arena.RxRelease(f)
}

func TestPumpStopsOnContext(t *testing.T) {
	var acct accounting.Counters
	// Soak generator that never finishes on its own.
	gen := generator.NewStream(payload.NewRandom(1500, 1), 1400)
	p := New(gen, scheduler.Soak{}, datapath.NewDiscard(1500), &acct)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Run(ctx); err != nil {
		t.Fatalf("Run on cancelled ctx: %v", err)
	}
}
