// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import "net"

// TCPSocket is a kernel-stack transmit datapath over a connected TCP socket.
// Like UDPSocket it is part of the portable "socket" backend family
// (DESIGN.md §5.1). A whole committed batch is written in one vectored write so
// the kernel can segment it (TSO) — TCP is a stream, so frame boundaries don't
// matter on the wire, and one large write per batch is what reaches line rate.
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

// TxCommit writes the whole batch in one vectored (writev) call, so the kernel
// queues it as one large chunk and segments it with TSO.
func (s *TCPSocket) TxCommit(frames []Frame) (int, error) {
	bufs := make(net.Buffers, 0, len(frames))
	sent := 0
	for i := range frames {
		if frames[i].Len <= 0 {
			continue
		}
		bufs = append(bufs, frames[i].Data[:frames[i].Len])
		sent++
	}
	if len(bufs) > 0 {
		if _, err := bufs.WriteTo(s.conn); err != nil {
			s.pool.release()
			return 0, err
		}
	}
	s.pool.release()
	return sent, nil
}

// Close closes the socket.
func (s *TCPSocket) Close() error { return s.conn.Close() }

// TCPDiag is a snapshot of a sender socket's kernel TCP_INFO, for link profiling
// (see Diagnoser). All counters are sender-side; RTTs are in microseconds and the
// windows are in segments.
type TCPDiag struct {
	TotalRetrans uint32 // cumulative retransmitted segments
	Lost         uint32 // segments currently considered lost
	RttUs        uint32 // smoothed round-trip time (µs)
	RttvarUs     uint32 // round-trip time variance (µs)
	SndCwnd      uint32 // congestion window (segments)
	SndSsthresh  uint32 // slow-start threshold (segments)
}

// Diagnoser is an optional datapath capability: report transport health (TCP_INFO
// for the TCP datapath). The agent reads it at telemetry boundaries so the
// controller can surface retransmits/RTT/cwnd in the run summary.
type Diagnoser interface {
	Diag() (TCPDiag, bool)
}
