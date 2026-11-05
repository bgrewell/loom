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

// GRO receive sizing: each coalesced recvmsg can carry up to groBufSize bytes
// (the kernel's UDP GRO ceiling), and a poll reads up to groMsgs of them.
const (
	groBufSize = 65536
	groMsgs    = 32
)

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
	// gro, when the kernel allows it, has the socket coalesce consecutive
	// same-flow datagrams into one recvmsg; rxPollGRO splits them back using the
	// UDP_GRO segment size. groM are the big reused receive buffers (with OOB for
	// the control message) and rxFrames the split result. Off => plain recvmmsg.
	gro      bool
	groM     []ipv4.Message
	rxFrames []Frame
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
	l := &UDPListener{conn: uc, batch: ipv4.NewPacketConn(uc), pool: newFramePool(defaultPoolDepth, frameSize)}
	if enableUDPGRO(uc) {
		l.gro = true
		l.groM = make([]ipv4.Message, groMsgs)
		for i := range l.groM {
			l.groM[i].Buffers = [][]byte{make([]byte, groBufSize)}
			l.groM[i].OOB = make([]byte, unix.CmsgSpace(4))
		}
		l.rxFrames = make([]Frame, 0, groMsgs*48)
	}
	return l, nil
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
	if l.gro {
		return l.rxPollGRO()
	}
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

// rxPollGRO reads a batch of (possibly GRO-coalesced) messages and splits each
// back into its datagrams using the UDP_GRO segment size, so the receiver
// accounts individual datagrams while the kernel did one stack traversal per
// coalesced read. A message with no UDP_GRO control data is a single datagram.
func (l *UDPListener) rxPollGRO() ([]Frame, error) {
	_ = l.conn.SetReadDeadline(time.Now().Add(recvDeadline))
	n, err := l.batch.ReadBatch(l.groM, unix.MSG_WAITFORONE)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixNano()
	l.rxFrames = l.rxFrames[:0]
	for i := 0; i < n; i++ {
		m := &l.groM[i]
		total := m.N
		if total <= 0 {
			continue
		}
		seg := total
		if m.NN > 0 {
			if s := groSegSize(m.OOB[:m.NN]); s > 0 {
				seg = s
			}
		}
		buf := m.Buffers[0]
		src := addrPort(m.Addr)
		for off := 0; off < total; off += seg {
			end := off + seg
			if end > total {
				end = total
			}
			l.rxFrames = append(l.rxFrames, Frame{Data: buf[off:end], Len: end - off, Meta: Meta{Nanos: now, Src: src}})
		}
	}
	return l.rxFrames, nil
}

// RxRelease returns polled frames to the pool (a no-op in GRO mode, whose big
// buffers are reused in place on the next poll).
func (l *UDPListener) RxRelease([]Frame) {
	if !l.gro {
		l.pool.release()
	}
}

// Close releases the socket.
func (l *UDPListener) Close() error { return l.conn.Close() }

func addrPort(a net.Addr) netip.AddrPort {
	if u, ok := a.(*net.UDPAddr); ok {
		return u.AddrPort()
	}
	return netip.AddrPort{}
}
