// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import "net"

// UDPSocket is the default kernel-stack transmit datapath: a connected UDP
// socket. It is the portable baseline backend (DESIGN.md §5.1); kernel-bypass
// backends (AF_XDP/DPDK) arrive in later phases. It is not zero-copy — the
// kernel copies on write — but presents the frame interface so the hot path is
// uniform across backends.
type UDPSocket struct {
	conn net.Conn
	pool *framePool
}

// DialUDP connects a UDP socket to addr (host:port) with frameSize-byte frames.
func DialUDP(addr string, frameSize int) (*UDPSocket, error) {
	c, err := net.Dial("udp", addr)
	if err != nil {
		return nil, err
	}
	return &UDPSocket{conn: c, pool: newFramePool(defaultPoolDepth, frameSize)}, nil
}

// Name implements TxDatapath.
func (*UDPSocket) Name() string { return "udp" }

// Caps implements TxDatapath.
func (*UDPSocket) Caps() Capabilities { return Capabilities{} }

// TxReserve hands out frames to fill.
func (s *UDPSocket) TxReserve(n int) []Frame { return s.pool.take(n) }

// TxCommit writes each filled frame as one datagram.
func (s *UDPSocket) TxCommit(frames []Frame) (int, error) {
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
func (s *UDPSocket) Close() error { return s.conn.Close() }
