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

// Run drives the loop until the context is done or the generator finishes,
// returning nil on a clean stop. It returns the datapath's error on a send
// failure. Nothing inside the loop allocates.
//
// It paces one packet per iteration, reserves a frame, lets the generator fill
// it, and commits it. Batched pacing (reserve/fill/commit N at once) is a
// follow-on that the interface already supports.
func (p *Pump) Run(ctx context.Context) error {
	for {
		if !p.sched.Pace(ctx) {
			return nil
		}
		frames := p.dp.TxReserve(1)
		if len(frames) == 0 {
			continue // ring momentarily full; pace and retry
		}
		n, done := p.gen.Next(frames[0].Data)
		if n > 0 {
			frames[0].Len = n
			sent, err := p.dp.TxCommit(frames[:1])
			if err != nil {
				return err
			}
			if sent > 0 {
				p.acct.Add(uint64(n))
				if p.events != nil {
					p.events.Push(log.Event{Code: log.EventSent, Value: uint64(n), Nanos: time.Now().UnixNano()})
				}
			}
		} else {
			_, _ = p.dp.TxCommit(frames[:0]) // release the reserved-but-unused frame
		}
		if done {
			return nil
		}
	}
}
