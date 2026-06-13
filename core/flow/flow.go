// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package flow is the atomic unit of traffic: a generator paced by a scheduler,
// sent over a datapath, accounted, and bounded by a stop condition. See
// DESIGN.md §5.
package flow

import (
	"context"
	"time"

	"github.com/bgrewell/loom/core/accounting"
	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/generator"
	"github.com/bgrewell/loom/core/pump"
	"github.com/bgrewell/loom/core/scheduler"
)

// Stop bounds a flow's run. A zero Stop runs until the context is cancelled
// (until-stopped). When more than one bound is set, whichever is reached first
// ends the flow.
type Stop struct {
	After  time.Duration // stop after this much wall-clock time
	Volume uint64        // stop after this many bytes sent
	Count  uint64        // stop after this many packets sent
}

// Flow binds the data-plane components and a stop condition into a runnable unit.
type Flow struct {
	Generator generator.Generator
	Scheduler scheduler.Scheduler
	Datapath  datapath.Datapath
	MTU       int
	Stop      Stop

	acct accounting.Counters
}

// Counters exposes the flow's live byte/packet totals for sampling/reporting.
func (f *Flow) Counters() *accounting.Counters { return &f.acct }

// Run executes the flow until its stop condition or ctx cancellation. Returns
// nil on a clean stop, or the datapath's error on a send failure.
func (f *Flow) Run(ctx context.Context) error {
	gen := f.Generator
	if f.Stop.Volume > 0 || f.Stop.Count > 0 {
		gen = &limited{g: gen, maxBytes: f.Stop.Volume, maxPackets: f.Stop.Count}
	}
	if f.Stop.After > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.Stop.After)
		defer cancel()
	}
	return pump.New(gen, f.Scheduler, f.Datapath, &f.acct, f.MTU).Run(ctx)
}

// limited wraps a generator to report done once a byte or packet bound is hit.
// It keeps the stop logic out of the pump's hot loop.
type limited struct {
	g          generator.Generator
	maxBytes   uint64
	maxPackets uint64
	bytes      uint64
	packets    uint64
}

func (l *limited) Name() string { return l.g.Name() }

func (l *limited) Next(buf []byte) (int, bool) {
	if (l.maxBytes > 0 && l.bytes >= l.maxBytes) ||
		(l.maxPackets > 0 && l.packets >= l.maxPackets) {
		return 0, true
	}
	n, done := l.g.Next(buf)
	l.bytes += uint64(n)
	l.packets++
	return n, done
}
