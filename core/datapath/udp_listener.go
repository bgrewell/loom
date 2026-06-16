// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import (
	"fmt"
	"net"
	"net/netip"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

// recvDeadline bounds a poll so the receive loop can observe context
// cancellation between polls.
const recvDeadline = 200 * time.Millisecond

// udpRcvBuf is the requested socket receive buffer (SO_RCVBUF). A large buffer
// absorbs bursts while the receiver batches reads, cutting drops at high pps.
// Best-effort: the kernel clamps to net.core.rmem_max.
const udpRcvBuf = 8 << 20

// UDPListener is a receive-side datapath: it binds a UDP port and reads inbound
// datagrams into pooled frames. Used by an agent acting as a flow's receiver
// after ephemeral-port negotiation.
//
// A poll reads the whole batch with one recvmmsg syscall (via ipv4.PacketConn's
// ReadBatch) instead of a recvfrom per datagram, so the per-packet syscall cost
// is amortized — the lever that lets the receiver keep up at high pps.
type UDPListener struct {
	conn  *net.UDPConn
	batch *ipv4.PacketConn
	pool  *framePool
	msgs  []ipv4.Message // reused scratch for ReadBatch
}

// ListenUDP binds a UDP socket at addr (use ":0" for an ephemeral port) with
// frameSize-byte receive frames.
func ListenUDP(addr string, frameSize int) (*UDPListener, error) {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return nil, err
	}
	uc, ok := pc.(*net.UDPConn)
	if !ok {
		_ = pc.Close()
		return nil, fmt.Errorf("udp: expected *net.UDPConn, got %T", pc)
	}
	_ = uc.SetReadBuffer(udpRcvBuf) // best-effort burst headroom
	return &UDPListener{conn: uc, batch: ipv4.NewPacketConn(uc), pool: newFramePool(defaultPoolDepth, frameSize)}, nil
}

// Port returns the bound local UDP port.
func (l *UDPListener) Port() int {
	if a, ok := l.conn.LocalAddr().(*net.UDPAddr); ok {
		return a.Port
	}
	return 0
}

// Name implements RxDatapath.
func (*UDPListener) Name() string { return "udp-listen" }

// Caps implements RxDatapath.
func (*UDPListener) Caps() Capabilities { return Capabilities{} }

// RxPoll reads up to max datagrams in one recvmmsg. It blocks up to recvDeadline
// for the first datagram (MSG_WAITFORONE returns as soon as one arrives, with any
// others already queued), so a timeout with no datagrams returns a net.Error with
// Timeout()==true and the receiver loop can check cancellation.
func (l *UDPListener) RxPoll(max int) ([]Frame, error) {
	frames := l.pool.take(max)
	if len(frames) == 0 {
		return nil, nil
	}
	for len(l.msgs) < len(frames) {
		l.msgs = append(l.msgs, ipv4.Message{Buffers: make([][]byte, 1)})
	}
	for i := range frames {
		b := frames[i].Data
		l.msgs[i].Buffers[0] = b[:cap(b)]
	}
	_ = l.conn.SetReadDeadline(time.Now().Add(recvDeadline))
	n, err := l.batch.ReadBatch(l.msgs[:len(frames)], unix.MSG_WAITFORONE)
	if err != nil {
		l.pool.release()
		return nil, err
	}
	now := time.Now().UnixNano()
	for i := 0; i < n; i++ {
		frames[i].Len = l.msgs[i].N
		frames[i].Meta = Meta{Nanos: now, Src: addrPort(l.msgs[i].Addr)}
	}
	return frames[:n], nil
}

// RxRelease returns polled frames to the pool.
func (l *UDPListener) RxRelease([]Frame) { l.pool.release() }

// Close releases the socket.
func (l *UDPListener) Close() error { return l.conn.Close() }

func addrPort(a net.Addr) netip.AddrPort {
	if u, ok := a.(*net.UDPAddr); ok {
		return u.AddrPort()
	}
	return netip.AddrPort{}
}
