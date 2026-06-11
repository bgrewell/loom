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
}

// NewInterval returns a scheduler that releases one packet every d.
func NewInterval(d time.Duration) *Interval {
	return &Interval{every: d}
}

// Name implements Scheduler.
func (*Interval) Name() string { return "interval" }

// Pace blocks until the next gap elapses; false means stop.
func (i *Interval) Pace(ctx context.Context) bool {
	now := time.Now()
	if i.next.IsZero() {
		i.next = now
	}
	wait := i.next.Sub(now)
	i.next = i.next.Add(i.every)

	if wait <= 0 {
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	}

	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
