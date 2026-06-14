// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import "net"

// TCPSocket is a kernel-stack transmit datapath over a connected TCP socket.
// Like UDPSocket it is part of the portable "socket" backend family
// (DESIGN.md §5.1). Each committed frame is one stream write.
type TCPSocket struct {
	conn net.Conn
	pool *framePool
}

// DialTCP connects a TCP socket to addr (host:port) with frameSize-byte frames.
func DialTCP(addr string, frameSize int) (*TCPSocket, error) {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &TCPSocket{conn: c, pool: newFramePool(defaultPoolDepth, frameSize)}, nil
}

// Name implements TxDatapath.
func (*TCPSocket) Name() string { return "tcp" }

// Caps implements TxDatapath.
func (*TCPSocket) Caps() Capabilities { return Capabilities{} }

// TxReserve hands out frames to fill.
func (s *TCPSocket) TxReserve(n int) []Frame { return s.pool.take(n) }

// TxCommit writes each filled frame to the stream.
func (s *TCPSocket) TxCommit(frames []Frame) (int, error) {
	sent := 0
	for i := range frames {
		if frames[i].Len <= 0 {
			continue
		}
		if _, err := s.conn.Write(frames[i].Data[:frames[i].Len]); err != nil {
			s.pool.release()
			return sent, err
		}
		sent++
	}
	s.pool.release()
	return sent, nil
}

// Close closes the socket.
func (s *TCPSocket) Close() error { return s.conn.Close() }
