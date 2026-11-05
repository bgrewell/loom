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
	// gso, when the kernel allows it, lets a batch of equal-size datagrams go as
	// one segmentation-offload write instead of a sendmmsg: the stack is walked
	// once per chunk, not once per datagram. gsoSize is the segment size and
	// gsoBuf the reused packing scratch. Disabled where GSO is unavailable.
	gso     bool
	gsoSize int
	gsoBuf  []byte
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
	fs := frameSize
	if fs < 1 {
		fs = 1500
	}
	sock := &UDPSocket{conn: uc, batch: ipv4.NewPacketConn(uc), pool: newFramePool(defaultPoolDepth, frameSize), gsoSize: fs}
	sock.gso = enableUDPSegment(uc, fs)
	return sock, nil
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
	if s.gso {
		if sent, ok, err := s.txSegmented(frames); ok {
			s.pool.release()
			return sent, err
		}
	}
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

// txSegmented sends the batch with UDP GSO when it is segmentation-shaped: at
// least two filled frames, and every filled frame but the last exactly gsoSize.
// Filled frames are packed back-to-back and written in chunks bounded by 65535
// bytes and gsoMaxSegments, one syscall each; the kernel splits each chunk back
// into gsoSize datagrams. ok=false means the batch is not GSO-shaped, so the
// caller falls back to sendmmsg (e.g. a variable-size generator).
func (s *UDPSocket) txSegmented(frames []Frame) (sent int, ok bool, err error) {
	last, count := -1, 0
	for i := range frames {
		if frames[i].Len <= 0 {
			continue
		}
		count++
		last = i
	}
	if count < 2 {
		return 0, false, nil
	}
	for i := range frames {
		if frames[i].Len > 0 && i != last && frames[i].Len != s.gsoSize {
			return 0, false, nil // a short datagram mid-batch cannot be segmented
		}
	}
	maxSeg := gsoMaxSegments
	if m := 65535 / s.gsoSize; m < maxSeg {
		maxSeg = m
	}
	if maxSeg < 2 {
		return 0, false, nil // segment too large to batch; sendmmsg is as good
	}
	if cap(s.gsoBuf) < maxSeg*s.gsoSize {
		s.gsoBuf = make([]byte, maxSeg*s.gsoSize)
	}
	buf := s.gsoBuf
	off, seg := 0, 0
	flush := func() error {
		if seg == 0 {
			return nil
		}
		if _, werr := s.conn.Write(buf[:off]); werr != nil {
			return werr
		}
		sent += seg
		off, seg = 0, 0
		return nil
	}
	for i := range frames {
		if frames[i].Len <= 0 {
			continue
		}
		off += copy(buf[off:], frames[i].Data[:frames[i].Len])
		seg++
		if seg == maxSeg {
			if ferr := flush(); ferr != nil {
				return sent, true, ferr
			}
		}
	}
	if ferr := flush(); ferr != nil {
		return sent, true, ferr
	}
	return sent, true, nil
}

// Close closes the socket.
func (s *UDPSocket) Close() error { return s.conn.Close() }
