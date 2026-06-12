// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package pump is the data-plane inner loop: it draws packets from a generator,
// paces them with a scheduler, sends them over a datapath, and records them in
// accounting. The loop is allocation-free — its single buffer is allocated once
// before the loop. See DESIGN.md §5.4/§6 and docs/blueprints/schedulers.md.
package pump

import (
	"context"

	"github.com/bgrewell/loom/core/accounting"
	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/generator"
	"github.com/bgrewell/loom/core/scheduler"
)

const defaultMTU = 1500

// Pump binds a generator, scheduler, datapath, and counters into one runnable
// loop.
type Pump struct {
	gen   generator.Generator
	sched scheduler.Scheduler
	dp    datapath.Datapath
	acct  *accounting.Counters
	mtu   int
}

// New builds a Pump. mtu bounds the per-packet buffer; <1 uses the default.
func New(gen generator.Generator, sched scheduler.Scheduler, dp datapath.Datapath, acct *accounting.Counters, mtu int) *Pump {
	if mtu < 1 {
		mtu = defaultMTU
	}
	return &Pump{gen: gen, sched: sched, dp: dp, acct: acct, mtu: mtu}
}

// Run drives the loop until the context is done or the generator finishes,
// returning nil on a clean stop. It returns the datapath's error on a send
// failure. Nothing inside the loop allocates.
func (p *Pump) Run(ctx context.Context) error {
	buf := make([]byte, p.mtu)
	for {
		if !p.sched.Pace(ctx) {
			return nil
		}
		n, done := p.gen.Next(buf)
		if n > 0 {
			m, err := p.dp.Send(buf[:n])
			if err != nil {
				return err
			}
			p.acct.Add(uint64(m))
		}
		if done {
			return nil
		}
	}
}
