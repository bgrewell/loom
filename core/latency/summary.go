// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package latency

import (
	"time"

	"github.com/bgrewell/loom/core/stats"
)

// Summary aggregates a set of probe results into latency/loss statistics.
type Summary struct {
	Sent     uint64
	Received uint64
	Lost     uint64 // probes that timed out or errored
	LossPct  float64
	Min      time.Duration
	Max      time.Duration
	Mean     time.Duration
	StdDev   time.Duration
	Jitter   time.Duration
}

// Summarize computes a Summary over rs. Loss counts probes without an OK reply.
func Summarize(rs []Result) Summary {
	var s stats.Stream
	var j stats.Jitter
	var sum Summary

	sum.Sent = uint64(len(rs))
	for _, r := range rs {
		if r.State == StateOK {
			sum.Received++
			ns := float64(r.RTT.Nanoseconds())
			s.Add(ns)
			j.Add(ns)
		} else {
			sum.Lost++
		}
	}
	if sum.Sent > 0 {
		sum.LossPct = float64(sum.Lost) / float64(sum.Sent) * 100
	}
	if s.Count() > 0 {
		sum.Min = time.Duration(s.Min())
		sum.Max = time.Duration(s.Max())
		sum.Mean = time.Duration(s.Mean())
		sum.StdDev = time.Duration(s.StdDev())
		sum.Jitter = time.Duration(j.Mean())
	}
	return sum
}
