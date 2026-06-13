// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import (
	"errors"
	"net"
	"time"
)

// recvDeadline bounds each Recv so the receive loop can observe context
// cancellation between reads.
const recvDeadline = 200 * time.Millisecond

// UDPListener is a receive-side datapath: it binds a UDP port and reads inbound
// datagrams. Send is rejected (it is receive-only). Used by an agent acting as a
// flow's receiver after ephemeral-port negotiation.
type UDPListener struct {
	conn net.PacketConn
}

// ListenUDP binds a UDP socket at addr (use ":0" for an ephemeral port).
func ListenUDP(addr string) (*UDPListener, error) {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return nil, err
	}
	return &UDPListener{conn: pc}, nil
}

// Port returns the bound local UDP port.
func (l *UDPListener) Port() int {
	if a, ok := l.conn.LocalAddr().(*net.UDPAddr); ok {
		return a.Port
	}
	return 0
}

// Name implements Datapath.
func (*UDPListener) Name() string { return "udp-listen" }

// Caps implements Datapath.
func (*UDPListener) Caps() Capabilities { return Capabilities{} }

// Send is rejected; a listener is receive-only.
func (*UDPListener) Send([]byte) (int, error) {
	return 0, errors.New("datapath: udp-listen is receive-only")
}

// Recv reads one datagram into p, with a bounded deadline so callers stay
// responsive to cancellation. A deadline timeout returns a net.Error with
// Timeout() == true.
func (l *UDPListener) Recv(p []byte) (int, error) {
	_ = l.conn.SetReadDeadline(time.Now().Add(recvDeadline))
	n, _, err := l.conn.ReadFrom(p)
	return n, err
}

// Close releases the socket.
func (l *UDPListener) Close() error { return l.conn.Close() }
