// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package dgram

import (
	"net"
	"net/netip"
	"os"
	"sync"
	"time"
)

// inbound is one delivered datagram: payload copy, source endpoint, and the
// datapath's receive timestamp (zero when the frame carried none).
type inbound struct {
	payload []byte
	src     netip.AddrPort
	arrival time.Time
}

// packetConn is a dgram endpoint bound to one local port. ReadFrom pulls
// demultiplexed datagrams from the network's receive loop; WriteTo encodes
// real IPv4+UDP packets through the network's tx datapath.
type packetConn struct {
	n    *dgramNetwork
	port uint16
	ch   chan inbound
	done chan struct{}
	once sync.Once

	mu            sync.Mutex
	readDeadline  time.Time
	dlChanged     chan struct{} // closed+replaced by SetReadDeadline to wake blocked reads
	writeDeadline time.Time     // tracked for interface fidelity; writes never block
}

var _ MetaConn = (*packetConn)(nil)

// ReadFrom implements net.PacketConn. A datagram longer than p is truncated,
// as with UDP sockets.
func (c *packetConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, from, _, err := c.ReadFromMeta(p)
	return n, from, err
}

// ReadFromMeta implements MetaConn: ReadFrom plus the datagram's receive
// timestamp from the datapath frame's Meta (zero time.Time when the datapath
// did not stamp the frame).
func (c *packetConn) ReadFromMeta(p []byte) (int, net.Addr, time.Time, error) {
	in, err := c.readInbound()
	if err != nil {
		return 0, nil, time.Time{}, err
	}
	return copy(p, in.payload), udpAddr(in.src), in.arrival, nil
}

// readInbound dequeues one datagram, honoring the read deadline per the
// net.Conn contract: the deadline in force is re-armed whenever
// SetReadDeadline changes it, so a deadline set while this read is blocked
// (the standard SetReadDeadline(time.Now()) interrupt idiom) wakes it.
func (c *packetConn) readInbound() (inbound, error) {
	for {
		c.mu.Lock()
		dl, changed := c.readDeadline, c.dlChanged
		c.mu.Unlock()
		var timer *time.Timer
		var timeout <-chan time.Time
		if !dl.IsZero() {
			d := time.Until(dl)
			if d <= 0 {
				return inbound{}, c.opErr("read", os.ErrDeadlineExceeded)
			}
			timer = time.NewTimer(d)
			timeout = timer.C
		}
		select {
		case in := <-c.ch:
			if timer != nil {
				timer.Stop()
			}
			return in, nil
		case <-c.done:
			if timer != nil {
				timer.Stop()
			}
			return inbound{}, c.opErr("read", net.ErrClosed)
		case <-timeout:
			return inbound{}, c.opErr("read", os.ErrDeadlineExceeded)
		case <-changed:
			if timer != nil {
				timer.Stop()
			}
			// Deadline replaced mid-read: loop and re-arm with the new one.
		}
	}
}

// WriteTo implements net.PacketConn: it encodes p as a complete IPv4+UDP
// packet to addr (an IPv4 ip:port; *net.UDPAddr or any net.Addr whose String
// is an ip:port literal) and commits it to the tx datapath.
func (c *packetConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	dst, err := toAddrPort(addr)
	if err != nil {
		return 0, err
	}
	return c.writeTo(p, dst)
}

// writeTo sends p to dst from this conn's bound port.
func (c *packetConn) writeTo(p []byte, dst netip.AddrPort) (int, error) {
	select {
	case <-c.done:
		return 0, c.opErr("write", net.ErrClosed)
	default:
	}
	if err := c.n.send(c.port, dst, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close implements net.PacketConn; it unbinds the port and unblocks pending
// reads.
func (c *packetConn) Close() error {
	c.once.Do(func() {
		close(c.done)
		c.n.unregister(c.port, c)
	})
	return nil
}

// LocalAddr implements net.PacketConn.
func (c *packetConn) LocalAddr() net.Addr {
	return udpAddr(netip.AddrPortFrom(c.n.local, c.port))
}

// SetDeadline implements net.PacketConn.
func (c *packetConn) SetDeadline(t time.Time) error {
	_ = c.SetReadDeadline(t)
	return c.SetWriteDeadline(t)
}

// SetReadDeadline implements net.PacketConn. Per the net.Conn contract it
// applies to future reads and wakes any currently-blocked ReadFrom/Read so it
// re-arms against the new deadline.
func (c *packetConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadline = t
	close(c.dlChanged) // wake blocked reads to re-arm
	c.dlChanged = make(chan struct{})
	c.mu.Unlock()
	return nil
}

// SetWriteDeadline implements net.PacketConn. Writes never block, so this
// only records the deadline.
func (c *packetConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.writeDeadline = t
	c.mu.Unlock()
	return nil
}

// opErr wraps err in a net.OpError so callers get standard Timeout()/Is
// behavior.
func (c *packetConn) opErr(op string, err error) error {
	return &net.OpError{Op: op, Net: "udp", Addr: c.LocalAddr(), Err: err}
}

// conn is the connected view returned by DialContext("udp"): Write sends to
// the fixed remote, Read filters to datagrams from it (dropping others, as a
// connected UDP socket does). The embedded packetConn keeps ReadFrom/WriteTo
// and ReadFromMeta available, mirroring the netpath memory fabric.
type conn struct {
	*packetConn
	remote netip.AddrPort
}

var _ net.Conn = (*conn)(nil)

// Read implements net.Conn.
func (c *conn) Read(p []byte) (int, error) {
	for {
		in, err := c.readInbound()
		if err != nil {
			return 0, err
		}
		if in.src == c.remote {
			return copy(p, in.payload), nil
		}
	}
}

// Write implements net.Conn.
func (c *conn) Write(p []byte) (int, error) { return c.writeTo(p, c.remote) }

// RemoteAddr implements net.Conn.
func (c *conn) RemoteAddr() net.Addr { return udpAddr(c.remote) }

// toAddrPort converts a destination net.Addr to an IPv4 netip.AddrPort,
// accepting *net.UDPAddr directly and otherwise parsing addr.String() as an
// ip:port literal (no name resolution).
func toAddrPort(addr net.Addr) (netip.AddrPort, error) {
	if addr == nil {
		return netip.AddrPort{}, &net.AddrError{Err: "nil address"}
	}
	if u, ok := addr.(*net.UDPAddr); ok {
		ap := u.AddrPort()
		a := ap.Addr().Unmap()
		if !a.Is4() {
			return netip.AddrPort{}, &net.AddrError{Err: "dgram is IPv4-only", Addr: addr.String()}
		}
		return netip.AddrPortFrom(a, ap.Port()), nil
	}
	return parseIPv4Target(addr.String())
}
