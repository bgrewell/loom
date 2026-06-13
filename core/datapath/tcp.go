// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import "net"

// TCPSocket is a kernel-stack datapath over a connected TCP socket. Like
// UDPSocket it is part of the portable "socket" backend family (DESIGN.md §5.1).
type TCPSocket struct {
	conn net.Conn
}

// DialTCP connects a TCP socket to addr (host:port).
func DialTCP(addr string) (*TCPSocket, error) {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &TCPSocket{conn: c}, nil
}

// Name implements Datapath.
func (*TCPSocket) Name() string { return "tcp" }

// Caps implements Datapath.
func (*TCPSocket) Caps() Capabilities { return Capabilities{} }

// Send writes bytes to the stream.
func (s *TCPSocket) Send(p []byte) (int, error) { return s.conn.Write(p) }

// Recv reads bytes from the stream into p.
func (s *TCPSocket) Recv(p []byte) (int, error) { return s.conn.Read(p) }

// Close closes the socket.
func (s *TCPSocket) Close() error { return s.conn.Close() }
