// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

//go:build loom_nonetstack

package netstack

import (
	"net/netip"

	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/netpath"
)

// Stack is a stub: this build carries the loom_nonetstack tag, which omits
// gVisor entirely for minimal agents. No Stack can be constructed — New
// always returns ErrDisabled — so no method is ever reached on a live value;
// they exist to keep the package API identical across builds.
type Stack struct{}

// New always returns ErrDisabled under the loom_nonetstack build tag.
func New(cfg Config, tx datapath.TxDatapath, rx datapath.RxDatapath) (*Stack, error) {
	return nil, ErrDisabled
}

// AddAddress always returns ErrDisabled under the loom_nonetstack build tag.
func (*Stack) AddAddress(netip.Addr) error { return ErrDisabled }

// RemoveAddress always returns ErrDisabled under the loom_nonetstack build tag.
func (*Stack) RemoveAddress(netip.Addr) error { return ErrDisabled }

// Network returns nil under the loom_nonetstack build tag: New never
// constructs a Stack, so this is unreachable on a live value.
func (*Stack) Network(netip.Addr) netpath.Network { return nil }

// Stats is the Stack's datapath-edge counter snapshot (stub mirror — see the
// non-stub file for field semantics).
type Stats struct {
	// RxDroppedNonIP counts inbound frames dropped for being neither IPv4 nor
	// IPv6.
	RxDroppedNonIP uint64
}

// Stats returns a zero snapshot under the loom_nonetstack build tag.
func (*Stack) Stats() Stats { return Stats{} }

// Close is a no-op under the loom_nonetstack build tag.
func (*Stack) Close() error { return nil }
