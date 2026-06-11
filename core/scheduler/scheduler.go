// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package scheduler paces packets within a single flow (intra-flow rate
// control). See docs/blueprints/schedulers.md and DESIGN.md §5.2.
package scheduler

import "context"

// Scheduler decides when the next packet in a flow should be sent.
//
// Pace blocks until the next packet is due and returns true, or returns false
// when the flow should stop (e.g. the context was cancelled). Implementations
// must be allocation-free on this hot path.
type Scheduler interface {
	// Name returns the scheduler's registry identifier.
	Name() string
	// Pace blocks until the next send is due; false means stop.
	Pace(ctx context.Context) bool
}
