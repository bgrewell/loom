// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package scheduler paces packets within a single flow (intra-flow rate
// control). See docs/blueprints/schedulers.md and DESIGN.md §5.2.
package scheduler

import "context"

// Scheduler decides when the next packets in a flow should be sent.
//
// Pace blocks until at least one packet is due, then reports how many may be
// released now — between 1 and max — or ok=false when the flow should stop (e.g.
// the context was cancelled). A rate scheduler returns 1 after each gap; an
// unpaced (soak) scheduler returns max so the pump can batch sends and amortize
// the per-packet datapath cost. Implementations must be allocation-free on this
// hot path.
type Scheduler interface {
	// Name returns the scheduler's registry identifier.
	Name() string
	// Pace blocks until a send is due and returns how many (1..max) may go now;
	// ok=false means stop.
	Pace(ctx context.Context, max int) (n int, ok bool)
}
