// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package pump

import (
	"testing"

	"github.com/bgrewell/loom/core/accounting"
	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/generator"
	"github.com/bgrewell/loom/core/log"
	"github.com/bgrewell/loom/core/payload"
)

// step runs one pump iteration's work (no scheduler wait) over a TxDatapath —
// the real reserve/fill/commit/account hot path of Pump.Run, optionally emitting
// an event.
func step(gen generator.Generator, dp datapath.TxDatapath, acct *accounting.Counters, events *log.Ring) {
	frames := dp.TxReserve(1)
	n, _ := gen.Next(frames[0].Data)
	frames[0].Len = n
	_, _ = dp.TxCommit(frames[:1])
	acct.Add(uint64(n))
	if events != nil {
		events.Push(log.Event{Code: log.EventSent, Value: uint64(n)})
	}
}

func newStep() (generator.Generator, datapath.TxDatapath, *accounting.Counters) {
	return generator.NewStream(payload.NewRandom(1500, 1), 1400),
		datapath.SinglePacketTx(datapath.Discard{}, 1500), &accounting.Counters{}
}

// TestPumpStepZeroAllocs is the hot-path gate: a pump step must allocate nothing,
// with logging OFF and ON. An allocation creeping in fails the build (DESIGN §6).
func TestPumpStepZeroAllocs(t *testing.T) {
	gen, dp, acct := newStep()
	ring := log.NewRing(4096)
	drainNonblock := func() { // keep the ring from staying full during the run
		for {
			if _, ok := ring.Pop(); !ok {
				return
			}
		}
	}

	if a := testing.AllocsPerRun(1000, func() { step(gen, dp, acct, nil) }); a != 0 {
		t.Fatalf("step (no logging) allocs = %v, want 0", a)
	}
	if a := testing.AllocsPerRun(1000, func() {
		step(gen, dp, acct, ring)
		drainNonblock()
	}); a != 0 {
		t.Fatalf("step (with logging) allocs = %v, want 0", a)
	}
}

// TestLoggingNeverBlocks asserts the logging invariant's core property: emitting
// on the hot path never blocks, even with no drainer and a full ring.
func TestLoggingNeverBlocks(t *testing.T) {
	gen, dp, acct := newStep()
	ring := log.NewRing(8) // tiny; will fill immediately, never drained
	for i := 0; i < 100000; i++ {
		step(gen, dp, acct, ring) // must not block once full
	}
	if ring.Dropped() == 0 {
		t.Fatal("expected drops once the ring filled")
	}
}

func BenchmarkPumpStep(b *testing.B) {
	gen, dp, acct := newStep()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		step(gen, dp, acct, nil)
	}
}

func BenchmarkPumpStepWithLogging(b *testing.B) {
	gen, dp, acct := newStep()
	ring := log.NewRing(1 << 16)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		step(gen, dp, acct, ring)
		if i&0x3fff == 0 {
			for {
				if _, ok := ring.Pop(); !ok {
					break
				}
			}
		}
	}
}
