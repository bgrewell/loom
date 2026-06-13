// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package report turns a flow's live counters into streaming interval reports
// and an end-of-run summary, emitted to a pluggable sink (human/json/...).
// See DESIGN.md §7.
package report

import (
	"context"
	"time"

	"github.com/bgrewell/loom/core/accounting"
)

// Sample is a point-in-time reading during a run.
type Sample struct {
	Elapsed    time.Duration
	Bytes      uint64
	Packets    uint64
	BitsPerSec float64 // windowed throughput at this sample
}

// Summary is the end-of-run total.
type Summary struct {
	Duration      time.Duration
	Bytes         uint64
	Packets       uint64
	AvgBitsPerSec float64
}

// Reporter consumes interval samples and a final summary.
type Reporter interface {
	Sample(Sample)
	Summary(Summary)
}

// Collect samples c every interval and emits to r until done is closed or ctx is
// cancelled, then emits and returns the final Summary.
func Collect(ctx context.Context, c *accounting.Counters, interval time.Duration, r Reporter, done <-chan struct{}) Summary {
	start := time.Now()
	sampler := accounting.NewSampler(c, 64)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	last := start
	for {
		select {
		case <-done:
			return finish(start, c, r)
		case <-ctx.Done():
			return finish(start, c, r)
		case now := <-ticker.C:
			elapsed := now.Sub(last)
			last = now
			bps := sampler.Sample(elapsed)
			r.Sample(Sample{
				Elapsed:    now.Sub(start),
				Bytes:      c.Bytes(),
				Packets:    c.Packets(),
				BitsPerSec: bps,
			})
		}
	}
}

func finish(start time.Time, c *accounting.Counters, r Reporter) Summary {
	d := time.Since(start)
	b, p := c.Bytes(), c.Packets()
	var avg float64
	if d > 0 {
		avg = float64(b) * 8 / d.Seconds()
	}
	s := Summary{Duration: d, Bytes: b, Packets: p, AvgBitsPerSec: avg}
	r.Summary(s)
	return s
}
