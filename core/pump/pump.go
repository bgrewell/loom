// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package pump is the data-plane inner loop: it draws packets from a generator,
// paces them with a scheduler, sends them over a datapath, and records them in
// accounting. The loop is allocation-free — its single buffer is allocated once
// before the loop, and optional event logging goes through a non-blocking ring.
// See DESIGN.md §5.4/§6 and docs/blueprints/schedulers.md.
package pump

import (
	"context"
	"time"

	"github.com/bgrewell/loom/core/accounting"
	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/generator"
	"github.com/bgrewell/loom/core/log"
	"github.com/bgrewell/loom/core/scheduler"
)

// Pump binds a generator, scheduler, datapath, and counters into one runnable
// loop.
type Pump struct {
	gen    generator.Generator
	sched  scheduler.Scheduler
	dp     datapath.TxDatapath
	acct   *accounting.Counters
	events *log.Ring // optional per-packet event sink; nil disables it
}

// Option configures a Pump.
type Option func(*Pump)

// WithEvents emits a per-packet event into r on the hot path. r must be a
// single-consumer ring drained off the data plane. The push is non-blocking and
// allocation-free; if r is full the event is dropped and counted.
func WithEvents(r *log.Ring) Option {
	return func(p *Pump) { p.events = r }
}

// New builds a Pump over a TxDatapath. The generator writes straight into
// datapath-owned frames, so a zero-copy backend never copies packet bytes.
func New(gen generator.Generator, sched scheduler.Scheduler, dp datapath.TxDatapath, acct *accounting.Counters, opts ...Option) *Pump {
	p := &Pump{gen: gen, sched: sched, dp: dp, acct: acct}
	for _, o := range opts {
		o(p)
	}
	return p
}

// txBatch bounds how many packets the pump reserves/fills/commits per iteration
// when the scheduler permits a burst (soak). A rate scheduler returns 1, so
// paced flows are unaffected; an unpaced flow batches to amortize the per-packet
// datapath cost (one syscall per batch instead of per packet).
const txBatch = 64

// Run drives the loop until the context is done or the generator finishes,
// returning nil on a clean stop. It returns the datapath's error on a send
// failure. Nothing inside the loop allocates.
//
// Each iteration asks the scheduler how many packets may go now, reserves that
// many datapath frames, lets the generator fill them, and commits them as one
// batch. The generator writes straight into the datapath's frames, so a
// zero-copy backend never copies packet bytes.
func (p *Pump) Run(ctx context.Context) error {
	for {
		want, ok := p.sched.Pace(ctx, txBatch)
		if !ok {
			return nil
		}
		if want < 1 {
			want = 1
		}
		frames := p.dp.TxReserve(want)
		if len(frames) == 0 {
			continue // ring momentarily full; pace and retry
		}
		count, done := 0, false
		for i := range frames {
			n, d := p.gen.Next(frames[i].Data)
			if n <= 0 {
				done = d
				break
			}
			frames[i].Len = n
			count++
			if d {
				done = true
				break
			}
		}
		if count > 0 {
			sent, err := p.dp.TxCommit(frames[:count])
			if err != nil {
				return err
			}
			for i := 0; i < sent; i++ {
				p.acct.Add(uint64(frames[i].Len))
				if p.events != nil {
					p.events.Push(log.Event{Code: log.EventSent, Value: uint64(frames[i].Len), Nanos: time.Now().UnixNano()})
				}
			}
		} else {
			_, _ = p.dp.TxCommit(frames[:0]) // release reserved-but-unused frames
		}
		if done {
			return nil
		}
	}
}
