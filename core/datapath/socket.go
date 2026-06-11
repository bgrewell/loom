// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import "net"

// UDPSocket is the default kernel-stack datapath: a connected UDP socket. It is
// the portable baseline backend (DESIGN.md §5.1); higher-rate backends
// (AF_PACKET/AF_XDP/DPDK) arrive in later phases.
type UDPSocket struct {
	conn net.Conn
}

// DialUDP connects a UDP socket to addr (host:port).
func DialUDP(addr string) (*UDPSocket, error) {
	c, err := net.Dial("udp", addr)
	if err != nil {
		return nil, err
	}
	return &UDPSocket{conn: c}, nil
}

// Name implements Datapath.
func (*UDPSocket) Name() string { return "socket" }

// Caps implements Datapath.
func (*UDPSocket) Caps() Capabilities { return Capabilities{} }

// Send writes one datagram.
func (s *UDPSocket) Send(p []byte) (int, error) { return s.conn.Write(p) }

// Recv reads one datagram into p.
func (s *UDPSocket) Recv(p []byte) (int, error) { return s.conn.Read(p) }

// Close closes the socket.
func (s *UDPSocket) Close() error { return s.conn.Close() }
