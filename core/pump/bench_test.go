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

// step runs one pump iteration's work (no scheduler wait) over the discard sink,
// optionally emitting an event — mirroring Pump.Run's hot path.
func step(gen generator.Generator, dp datapath.Datapath, acct *accounting.Counters, buf []byte, events *log.Ring) {
	n, _ := gen.Next(buf)
	m, _ := dp.Send(buf[:n])
	acct.Add(uint64(m))
	if events != nil {
		events.Push(log.Event{Code: log.EventSent, Value: uint64(m)})
	}
}

func newStep() (generator.Generator, datapath.Datapath, *accounting.Counters, []byte) {
	return generator.NewStream(payload.NewRandom(1500, 1), 1400),
		datapath.Discard{}, &accounting.Counters{}, make([]byte, 1500)
}

// TestPumpStepZeroAllocs is the hot-path gate: a pump step must allocate nothing,
// with logging OFF and ON. An allocation creeping in fails the build (DESIGN §6).
func TestPumpStepZeroAllocs(t *testing.T) {
	gen, dp, acct, buf := newStep()
	ring := log.NewRing(4096)
	drainNonblock := func() { // keep the ring from staying full during the run
		for {
			if _, ok := ring.Pop(); !ok {
				return
			}
		}
	}

	if a := testing.AllocsPerRun(1000, func() { step(gen, dp, acct, buf, nil) }); a != 0 {
		t.Fatalf("step (no logging) allocs = %v, want 0", a)
	}
	if a := testing.AllocsPerRun(1000, func() {
		step(gen, dp, acct, buf, ring)
		drainNonblock()
	}); a != 0 {
		t.Fatalf("step (with logging) allocs = %v, want 0", a)
	}
}

// TestLoggingNeverBlocks asserts the logging invariant's core property: emitting
// on the hot path never blocks, even with no drainer and a full ring.
func TestLoggingNeverBlocks(t *testing.T) {
	gen, dp, acct, buf := newStep()
	ring := log.NewRing(8) // tiny; will fill immediately, never drained
	for i := 0; i < 100000; i++ {
		step(gen, dp, acct, buf, ring) // must not block once full
	}
	if ring.Dropped() == 0 {
		t.Fatal("expected drops once the ring filled")
	}
}

func BenchmarkPumpStep(b *testing.B) {
	gen, dp, acct, buf := newStep()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		step(gen, dp, acct, buf, nil)
	}
}

func BenchmarkPumpStepWithLogging(b *testing.B) {
	gen, dp, acct, buf := newStep()
	ring := log.NewRing(1 << 16)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		step(gen, dp, acct, buf, ring)
		if i&0x3fff == 0 {
			for {
				if _, ok := ring.Pop(); !ok {
					break
				}
			}
		}
	}
}
