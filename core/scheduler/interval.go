// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"context"
	"time"
)

// Interval paces packets at a fixed inter-packet gap. It tracks an absolute
// next-send time so timing does not drift with per-call jitter.
//
// TODO(perf): replace the timer wait with the sleep-coarse-then-spin technique
// (see docs/blueprints/schedulers.md) for sub-millisecond accuracy.
type Interval struct {
	every time.Duration
	next  time.Time
	now   func() time.Time // injectable clock (defaults to time.Now)
}

// NewInterval returns a scheduler that releases one packet every d.
func NewInterval(d time.Duration) *Interval {
	return &Interval{every: d, now: time.Now}
}

// Name implements Scheduler.
func (*Interval) Name() string { return "interval" }

// Pace blocks until the next gap elapses, then releases one packet (strict
// pacing, so max is ignored); ok=false means stop.
func (i *Interval) Pace(ctx context.Context, _ int) (int, bool) {
	now := i.now()
	if i.next.IsZero() {
		i.next = now
	}
	wait := i.next.Sub(now)
	if wait < 0 {
		// Fell behind (slow send / GC pause): drop the accrued deficit instead
		// of releasing a back-to-back burst to "catch up", which would spike the
		// rate well past the target. Re-baseline to now.
		i.next = now
		wait = 0
	}
	i.next = i.next.Add(i.every)

	if wait <= 0 {
		select {
		case <-ctx.Done():
			return 0, false
		default:
			return 1, true
		}
	}

	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return 0, false
	case <-t.C:
		return 1, true
	}
}
