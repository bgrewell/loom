// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package emul

import (
	"context"
	"math/rand"
	"time"

	"github.com/bgrewell/loom/core/accounting"
	"github.com/bgrewell/loom/core/datapath"
)

// Step is one unit of an application's behavior: send an object of Size bytes,
// then wait Think before the next step. Both are distributions sampled per step.
type Step struct {
	Size  Dist // object size in bytes
	Think Dist // gap after the object, in nanoseconds
}

// BehaviorScript is the sequence of steps an emulation compiles to. The Runner
// repeats the whole script until the flow's stop condition, so a single-step
// script (VoIP, Prometheus) is a steady cadence and a multi-step script
// (HTTPS session, SSH) is a repeating session.
type BehaviorScript []Step

// Runner executes a BehaviorScript over a transmit datapath, accounting bytes
// and packets — the emulation counterpart to the pump. It implements
// flow.Runner. Each object is chunked into mtu-sized packets sent back to back;
// think-time gaps separate steps. Reproducible given the seed.
type Runner struct {
	script  BehaviorScript
	dp      datapath.TxDatapath
	mtu     int
	after   time.Duration
	count   uint64
	volume  uint64
	rng     *rand.Rand
	pattern []byte
	acct    accounting.Counters
}

// NewRunner builds an emulation runner. after/count/volume are the flow's stop
// condition (any reached ends the run; all zero = until ctx is cancelled).
func NewRunner(script BehaviorScript, dp datapath.TxDatapath, mtu int, after time.Duration, count, volume uint64, seed int64) *Runner {
	if mtu < 1 {
		mtu = 1400
	}
	p := make([]byte, mtu)
	for i := range p {
		p[i] = 0xA5
	}
	return &Runner{
		script: script, dp: dp, mtu: mtu,
		after: after, count: count, volume: volume,
		rng: rand.New(rand.NewSource(seed)), pattern: p,
	}
}

// Counters exposes the live byte/packet totals for sampling/reporting.
func (r *Runner) Counters() *accounting.Counters { return &r.acct }

// Run executes the script until the stop condition or ctx cancellation.
func (r *Runner) Run(ctx context.Context) error {
	if len(r.script) == 0 {
		return nil
	}
	if r.after > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.after)
		defer cancel()
	}
	for {
		for _, step := range r.script {
			if ctx.Err() != nil {
				return nil
			}
			if err := r.sendObject(ctx, int(step.Size.Sample(r.rng))); err != nil {
				return err
			}
			if r.stopReached() {
				return nil
			}
			if think := time.Duration(step.Think.Sample(r.rng)); think > 0 {
				t := time.NewTimer(think)
				select {
				case <-ctx.Done():
					t.Stop()
					return nil
				case <-t.C:
				}
			}
		}
	}
}

func (r *Runner) stopReached() bool {
	if r.count > 0 && r.acct.Packets() >= r.count {
		return true
	}
	if r.volume > 0 && r.acct.Bytes() >= r.volume {
		return true
	}
	return false
}

// sendObject transmits size bytes as one or more mtu-sized packets.
func (r *Runner) sendObject(ctx context.Context, size int) error {
	if size < 1 {
		size = 1 // every step sends at least one packet
	}
	for sent := 0; sent < size; {
		if ctx.Err() != nil {
			return nil
		}
		n := size - sent
		if n > r.mtu {
			n = r.mtu
		}
		frames := r.dp.TxReserve(1)
		if len(frames) == 0 {
			continue // ring momentarily full; retry
		}
		copy(frames[0].Data, r.pattern[:n])
		frames[0].Len = n
		if _, err := r.dp.TxCommit(frames[:1]); err != nil {
			return err
		}
		r.acct.Add(uint64(n))
		sent += n
		if r.stopReached() {
			return nil
		}
	}
	return nil
}
