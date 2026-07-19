// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package netpath_test

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/netpath"
)

// TestHostSourceBinding verifies a source-bound host network binds its local
// address on every path: UDP listen, TCP listen, and TCP dial.
func TestHostSourceBinding(t *testing.T) {
	lo := netip.MustParseAddr("127.0.0.1")
	h := netpath.Host(lo)
	defer h.Close()

	pc, err := h.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatalf("ListenPacket(udp, :0): %v", err)
	}
	defer pc.Close()
	ua, ok := pc.LocalAddr().(*net.UDPAddr)
	if !ok || !ua.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Errorf("udp LocalAddr = %v, want 127.0.0.1:*", pc.LocalAddr())
	}

	ln, err := h.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("Listen(tcp, :0): %v", err)
	}
	defer ln.Close()
	ta, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !ta.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Errorf("tcp listener Addr = %v, want 127.0.0.1:*", ln.Addr())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := h.DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("DialContext(tcp, %s): %v", ln.Addr(), err)
	}
	defer c.Close()
	da, ok := c.LocalAddr().(*net.TCPAddr)
	if !ok || !da.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Errorf("dialed LocalAddr = %v, want 127.0.0.1:*", c.LocalAddr())
	}
}

// TestHostExplicitHostRespected verifies an explicit host in the listen
// address wins over the network's bound source address.
func TestHostExplicitHostRespected(t *testing.T) {
	h := netpath.Host(netip.MustParseAddr("127.0.0.1"))
	defer h.Close()
	ln, err := h.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("Listen(tcp, localhost:0): %v", err)
	}
	defer ln.Close()
	// localhost resolves to a loopback address; the point is Listen must not
	// reject or rewrite an address the caller spelled out.
	ta, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !ta.IP.IsLoopback() {
		t.Errorf("listener Addr = %v, want a loopback address", ln.Addr())
	}
}
