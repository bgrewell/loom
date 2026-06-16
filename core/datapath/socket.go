// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import (
	"fmt"
	"net"

	"golang.org/x/net/ipv4"
)

// UDPSocket is the default kernel-stack transmit datapath: a connected UDP
// socket. It is the portable baseline backend (DESIGN.md §5.1); kernel-bypass
// backends (AF_XDP/DPDK) arrive in later phases. It is not zero-copy — the
// kernel copies on write — but presents the frame interface so the hot path is
// uniform across backends.
//
// A committed batch is sent with one sendmmsg syscall (via ipv4.PacketConn's
// WriteBatch) instead of a sendto per datagram, so the per-packet syscall cost is
// amortized across the batch — the lever that lifts the UDP send ceiling.
type UDPSocket struct {
	conn  *net.UDPConn
	batch *ipv4.PacketConn
	pool  *framePool
	msgs  []ipv4.Message // reused scratch for WriteBatch (alloc-free after warmup)
}

// DialUDP connects a UDP socket to addr (host:port) with frameSize-byte frames.
func DialUDP(addr string, frameSize int) (*UDPSocket, error) {
	c, err := net.Dial("udp", addr)
	if err != nil {
		return nil, err
	}
	uc, ok := c.(*net.UDPConn)
	if !ok {
		_ = c.Close()
		return nil, fmt.Errorf("udp: expected *net.UDPConn, got %T", c)
	}
	return &UDPSocket{conn: uc, batch: ipv4.NewPacketConn(uc), pool: newFramePool(defaultPoolDepth, frameSize)}, nil
}

// Name implements TxDatapath.
func (*UDPSocket) Name() string { return "udp" }

// Caps implements TxDatapath.
func (*UDPSocket) Caps() Capabilities { return Capabilities{} }

// TxReserve hands out frames to fill.
func (s *UDPSocket) TxReserve(n int) []Frame { return s.pool.take(n) }

// TxCommit sends every filled frame as a datagram in a single sendmmsg. Addr is
// nil because the socket is connected (kernel sends to the connected peer).
func (s *UDPSocket) TxCommit(frames []Frame) (int, error) {
	// Grow the scratch (each message holds a single buffer) to cover this batch.
	for len(s.msgs) < len(frames) {
		s.msgs = append(s.msgs, ipv4.Message{Buffers: make([][]byte, 1)})
	}
	n := 0
	for i := range frames {
		if frames[i].Len <= 0 {
			continue
		}
		s.msgs[n].Buffers[0] = frames[i].Data[:frames[i].Len]
		n++
	}
	if n == 0 {
		s.pool.release()
		return 0, nil
	}
	sent, err := s.batch.WriteBatch(s.msgs[:n], 0)
	s.pool.release()
	if err != nil {
		return sent, err
	}
	return sent, nil
}

// Close closes the socket.
func (s *UDPSocket) Close() error { return s.conn.Close() }
