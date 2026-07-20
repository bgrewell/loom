// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package netpath provides connection-oriented network access (net.Conn /
// net.PacketConn semantics) as an injectable component: kernel stack,
// UDP-over-datapath, gVisor-over-datapath, or in-memory test loopback.
//
// Network is the single connection-factory seam: everything that dials or
// listens (protocol engines, responders, emulations) does so through an
// injected Network rather than calling net.Dial/net.Listen directly, so the
// same code runs over the kernel stack, a datapath-backed network, or the
// in-memory test fabric unchanged.
package netpath

import (
	"context"
	"errors"
	"fmt"
	"net"
)

// Network is a connection factory: one place that dials and listens, so the
// backing stack (kernel, datapath, in-memory) is injectable. The network and
// address arguments follow net.Dial conventions ("tcp"/"udp", "host:port").
type Network interface {
	// Name returns the network's registry identifier.
	Name() string
	// DialContext opens an outbound connection, like net.Dialer.DialContext.
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	// ListenPacket binds a datagram endpoint, like net.ListenPacket.
	ListenPacket(network, address string) (net.PacketConn, error)
	// Listen binds a stream listener, like net.Listen.
	Listen(network, address string) (net.Listener, error)
	// Close releases the network's resources. Implementations document what
	// this tears down (host: nothing; memory: listeners and packet conns
	// created through this handle).
	Close() error
}

// ErrUnsupportedNetwork is the sentinel matched by errors.Is when a Network
// implementation does not support the requested network name — e.g. "unix"
// anywhere, "udp" passed to Listen, or "tcp" on a UDP-only datapath-backed
// network.
var ErrUnsupportedNetwork = errors.New("unsupported network")

// UnsupportedNetworkError reports which Network implementation rejected which
// network name. It matches ErrUnsupportedNetwork via errors.Is.
type UnsupportedNetworkError struct {
	Impl    string // Network implementation name, e.g. "host"
	Network string // requested network name, e.g. "unix"
}

// Error implements error.
func (e *UnsupportedNetworkError) Error() string {
	return fmt.Sprintf("netpath: %s: unsupported network %q", e.Impl, e.Network)
}

// Is reports whether target is ErrUnsupportedNetwork, so callers can match the
// sentinel without knowing the concrete type.
func (e *UnsupportedNetworkError) Is(target error) bool { return target == ErrUnsupportedNetwork }

// isStream reports whether network names a stream (TCP) network.
func isStream(network string) bool {
	switch network {
	case "tcp", "tcp4", "tcp6":
		return true
	}
	return false
}

// isPacket reports whether network names a datagram (UDP) network.
func isPacket(network string) bool {
	switch network {
	case "udp", "udp4", "udp6":
		return true
	}
	return false
}
