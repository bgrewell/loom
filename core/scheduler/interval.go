// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"context"
	"time"
)

// Interval paces packets at a target average of one per `every`. It releases
// bursts: each call returns however many packets are due since the last call
// (capped at the caller's max), so the data plane batches instead of paying a
// per-packet timer — the per-packet timer cannot pace at the microsecond gaps
// high rates require (its wakeup granularity dwarfs the gap), which capped a
// rate-limited flow far below its target. It tracks an absolute next-send time so
// the average rate doesn't drift, and after a stall it drops the accrued deficit
// rather than bursting past the target to "catch up".
//
// When nothing is due yet it waits with a coarse sleep followed by a short spin,
// which paces accurately at microsecond gaps where a plain timer cannot.
type Interval struct {
	every time.Duration
	next  time.Time
	now   func() time.Time // injectable clock (defaults to time.Now)
}

// NewInterval returns a scheduler with a target average of one packet every d.
func NewInterval(d time.Duration) *Interval {
	if d < 1 {
		d = 1
	}
	return &Interval{every: d, now: time.Now}
}

// Name implements Scheduler.
func (*Interval) Name() string { return "interval" }

// Pace releases up to max packets whose send time has passed, blocking until at
// least one is due. ok=false means the context was cancelled (stop).
func (i *Interval) Pace(ctx context.Context, max int) (int, bool) {
	if max < 1 {
		max = 1
	}
	now := i.now()
	if i.next.IsZero() {
		i.next = now
	}
	// Fell behind by more than one batch (slow send / GC pause): drop the deficit
	// so we pace forward from now instead of releasing a rate-spiking catch-up.
	if now.Sub(i.next) > time.Duration(max)*i.every {
		i.next = now
	}
	n := 0
	for n < max && !i.next.After(now) {
		n++
		i.next = i.next.Add(i.every)
	}
	if n > 0 {
		select {
		case <-ctx.Done():
			return 0, false
		default:
			return n, true
		}
	}
	// Nothing due yet: wait until the next packet's send time, then release one.
	if !i.waitUntil(ctx, i.next) {
		return 0, false
	}
	i.next = i.next.Add(i.every)
	return 1, true
}

// waitUntil sleeps coarsely to within a spin window of deadline, then busy-waits
// the remainder for microsecond accuracy. Returns false if ctx is cancelled.
func (i *Interval) waitUntil(ctx context.Context, deadline time.Time) bool {
	const spin = 200 * time.Microsecond
	if d := deadline.Sub(i.now()); d > spin {
		t := time.NewTimer(d - spin)
		select {
		case <-ctx.Done():
			t.Stop()
			return false
		case <-t.C:
		}
	}
	for i.now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
	}
	return true
}
