// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package flow

import (
	"context"
	"errors"
	"net"

	"github.com/bgrewell/loom/core/accounting"
	"github.com/bgrewell/loom/core/datapath"
)

// Runner is the common shape of a runnable flow side — a sending Flow or a
// receiving Receiver. The agent drives flows through this interface.
type Runner interface {
	Run(ctx context.Context) error
	Counters() *accounting.Counters
}

// rxBatch is how many frames the receiver polls per call. A backend may return
// fewer (or one, for the single-packet adapter).
const rxBatch = 64

// Receiver is the receive side of a flow: it drains a datapath and accounts
// inbound bytes/packets until the context is cancelled. Pairs with a sender on
// another agent after ephemeral-port negotiation.
type Receiver struct {
	dp   datapath.RxDatapath
	acct accounting.Counters
}

// NewReceiver returns a Receiver draining dp.
func NewReceiver(dp datapath.RxDatapath) *Receiver {
	return &Receiver{dp: dp}
}

// Counters exposes the received byte/packet totals.
func (r *Receiver) Counters() *accounting.Counters { return &r.acct }

// Run drains the datapath until ctx is cancelled. Read deadlines (a net timeout)
// are retried so cancellation is observed promptly; any other read error ends
// the loop. Polled frames are released back to the datapath after accounting.
func (r *Receiver) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		frames, err := r.dp.RxPoll(rxBatch)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return nil
		}
		for i := range frames {
			if frames[i].Len > 0 {
				r.acct.Add(uint64(frames[i].Len))
			}
		}
		r.dp.RxRelease(frames)
	}
}
