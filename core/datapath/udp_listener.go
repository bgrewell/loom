// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import (
	"net"
	"net/netip"
	"time"
)

// recvDeadline bounds the first read in each poll so the receive loop can observe
// context cancellation between polls.
const recvDeadline = 200 * time.Millisecond

// UDPListener is a receive-side datapath: it binds a UDP port and reads inbound
// datagrams into pooled frames. Used by an agent acting as a flow's receiver
// after ephemeral-port negotiation.
type UDPListener struct {
	conn net.PacketConn
	pool *framePool
}

// ListenUDP binds a UDP socket at addr (use ":0" for an ephemeral port) with
// frameSize-byte receive frames.
func ListenUDP(addr string, frameSize int) (*UDPListener, error) {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return nil, err
	}
	return &UDPListener{conn: pc, pool: newFramePool(defaultPoolDepth, frameSize)}, nil
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

// RxPoll reads up to max datagrams into frames. The first read blocks up to
// recvDeadline (so the caller can check cancellation on a timeout); subsequent
// reads drain only what is already queued. A timeout with no datagrams returns a
// net.Error with Timeout()==true.
func (l *UDPListener) RxPoll(max int) ([]Frame, error) {
	frames := l.pool.take(max)
	if len(frames) == 0 {
		return nil, nil
	}
	got := 0
	for got < len(frames) {
		if got == 0 {
			_ = l.conn.SetReadDeadline(time.Now().Add(recvDeadline))
		} else {
			_ = l.conn.SetReadDeadline(time.Now()) // non-blocking drain of the backlog
		}
		buf := frames[got].Data
		n, src, err := l.conn.ReadFrom(buf[:cap(buf)])
		if err != nil {
			if got > 0 {
				break // return what we have; the timeout is expected mid-drain
			}
			l.pool.release()
			return nil, err
		}
		frames[got].Len = n
		frames[got].Meta = Meta{Nanos: time.Now().UnixNano(), Src: addrPort(src)}
		got++
	}
	return frames[:got], nil
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
