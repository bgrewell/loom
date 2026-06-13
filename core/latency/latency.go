// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package latency measures round-trip latency with typed results and an interval
// sampling model. See docs/blueprints/latency-probe.md and DESIGN.md §7.
package latency

import (
	"context"
	"errors"
	"net"
	"time"
)

// State classifies a probe result.
type State int

const (
	// StateOK means a reply was received and RTT is valid.
	StateOK State = iota
	// StateTimeout means no reply arrived within the timeout.
	StateTimeout
	// StateError means the probe failed for another reason.
	StateError
)

// String renders the state.
func (s State) String() string {
	switch s {
	case StateOK:
		return "ok"
	case StateTimeout:
		return "timeout"
	default:
		return "error"
	}
}

// Result is one probe's outcome.
type Result struct {
	Time  time.Time
	Seq   uint64
	State State
	RTT   time.Duration
	Err   error
}

// Pinger performs a single round-trip for sequence seq and returns the RTT.
type Pinger interface {
	Ping(ctx context.Context, seq uint64) (time.Duration, error)
}

// Sampler drives a Pinger: each Interval it fires Probes pings spaced by Spacing
// (each bounded by Timeout), collects the batch of Results, and passes it to
// emit. It runs until ctx is cancelled.
type Sampler struct {
	Pinger   Pinger
	Interval time.Duration
	Probes   int
	Spacing  time.Duration
	Timeout  time.Duration
}

// Run executes the sampling loop until ctx is cancelled.
func (s *Sampler) Run(ctx context.Context, emit func([]Result)) {
	probes := s.Probes
	if probes < 1 {
		probes = 1
	}
	interval := s.Interval
	if interval <= 0 {
		interval = time.Second
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var seq uint64
	for {
		if ctx.Err() != nil {
			return
		}
		batch := make([]Result, 0, probes)
		for i := 0; i < probes; i++ {
			pctx, cancel := context.WithTimeout(ctx, timeout)
			rtt, err := s.Pinger.Ping(pctx, seq)
			cancel()
			batch = append(batch, classify(seq, rtt, err))
			seq++

			if i < probes-1 {
				if s.Spacing > 0 {
					select {
					case <-ctx.Done():
					case <-time.After(s.Spacing):
					}
				}
				if ctx.Err() != nil {
					break
				}
			}
		}
		// Emit only complete batches; a batch interrupted by cancellation is
		// dropped so every emitted batch holds exactly `probes` results.
		if len(batch) == probes {
			emit(batch)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func classify(seq uint64, rtt time.Duration, err error) Result {
	r := Result{Time: time.Now(), Seq: seq}
	switch {
	case err == nil:
		r.State, r.RTT = StateOK, rtt
	case isTimeout(err):
		r.State, r.Err = StateTimeout, err
	default:
		r.State, r.Err = StateError, err
	}
	return r
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return ne.Timeout()
	}
	return false
}
