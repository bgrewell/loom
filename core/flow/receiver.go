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

// Receiver is the receive side of a flow: it drains a datapath and accounts
// inbound bytes/packets until the context is cancelled. Pairs with a sender on
// another agent after ephemeral-port negotiation.
type Receiver struct {
	dp   datapath.Datapath
	mtu  int
	acct accounting.Counters
}

// NewReceiver returns a Receiver draining dp with the given read buffer size.
func NewReceiver(dp datapath.Datapath, mtu int) *Receiver {
	if mtu < 1 {
		mtu = 1500
	}
	return &Receiver{dp: dp, mtu: mtu}
}

// Counters exposes the received byte/packet totals.
func (r *Receiver) Counters() *accounting.Counters { return &r.acct }

// Run drains the datapath until ctx is cancelled. Read deadlines (a net timeout)
// are retried so cancellation is observed promptly; any other read error ends
// the loop.
func (r *Receiver) Run(ctx context.Context) error {
	buf := make([]byte, r.mtu)
	for {
		if ctx.Err() != nil {
			return nil
		}
		n, err := r.dp.Recv(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return nil
		}
		if n > 0 {
			r.acct.Add(uint64(n))
		}
	}
}
