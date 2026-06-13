// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package log

import (
	"context"
	"time"
)

// Handler consumes a drained event. It runs off the hot path, so it may do
// real work (format, ship, aggregate).
type Handler func(Event)

// Drainer moves events from a Ring to a Handler off the data plane. It polls the
// ring, draining it fully each pass, and does a final drain on shutdown.
type Drainer struct {
	ring   *Ring
	handle Handler
	poll   time.Duration
}

// NewDrainer returns a Drainer for ring calling h per event. poll <= 0 uses 1ms.
func NewDrainer(ring *Ring, h Handler, poll time.Duration) *Drainer {
	if poll <= 0 {
		poll = time.Millisecond
	}
	return &Drainer{ring: ring, handle: h, poll: poll}
}

// Run drains until ctx is cancelled, then drains any remaining events and exits.
func (d *Drainer) Run(ctx context.Context) {
	t := time.NewTicker(d.poll)
	defer t.Stop()
	for {
		d.drain()
		select {
		case <-ctx.Done():
			d.drain()
			return
		case <-t.C:
		}
	}
}

func (d *Drainer) drain() {
	for {
		e, ok := d.ring.Pop()
		if !ok {
			return
		}
		d.handle(e)
	}
}
