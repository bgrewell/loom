// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package netpath

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

// Memory returns a connected pair of in-memory Networks for tests: no kernel,
// NIC, or privileges. Both handles share one fabric, so a listener or packet
// conn bound through either handle is reachable from both — the pair exists so
// a test reads naturally (client dials a, server listens on b). Streams are
// net.Pipe-backed full-duplex conns rendezvoused through Listen/Accept;
// datagrams have packet-conn semantics with per-endpoint addresses.
//
// Semantic simplifications vs a kernel stack (deliberate, for tests):
//   - One flat port namespace routed by port only: host parts of addresses are
//     parsed and ignored, and addresses print as "mem:<port>". Ports must be
//     numeric (no service-name or DNS resolution).
//   - A datagram sent to an unbound port succeeds and vanishes (as UDP does
//     before any ICMP unreachable arrives); a full receive buffer drops the
//     datagram (memChanDepth per endpoint).
//   - Packet-conn read deadlines follow the net.Conn contract: setting a
//     deadline affects future reads and wakes any currently-blocked ReadFrom
//     with os.ErrDeadlineExceeded, matching what a kernel UDP socket does.
//     (Stream conns delegate to net.Pipe, which implements full deadline
//     semantics.)
//   - Closing a handle closes the listeners and packet conns created through
//     it, but — like the kernel — not established stream conns.
func Memory() (a, b Network) {
	f := newMemFabric()
	return f.handle(), f.handle()
}

const (
	// memFirstPort is where auto-assigned ports start (the IANA dynamic range).
	memFirstPort = 49152
	// memChanDepth is the receive-buffer depth: queued datagrams per packet
	// conn, and the accept backlog per listener.
	memChanDepth = 256
)

var (
	errConnRefused    = errors.New("connection refused")
	errAddrInUse      = errors.New("address already in use")
	errPortsExhausted = errors.New("dynamic ports exhausted")
)

// memAddr is a memory-fabric endpoint address. Its String form uses the fixed
// host literal "mem" because fabric routing is by port only.
type memAddr struct {
	net  string
	port int
}

// Network implements net.Addr.
func (a memAddr) Network() string { return a.net }

// String implements net.Addr.
func (a memAddr) String() string { return "mem:" + strconv.Itoa(a.port) }

// memFabric is the shared switchboard behind a Memory pair: one flat port
// namespace routing streams and datagrams between the handles. TCP and UDP
// ports are independent namespaces, as in a kernel stack.
type memFabric struct {
	mu        sync.Mutex
	nextPort  int
	listeners map[int]*memListener
	pconns    map[int]*memPacketConn
}

func newMemFabric() *memFabric {
	return &memFabric{
		nextPort:  memFirstPort - 1,
		listeners: make(map[int]*memListener),
		pconns:    make(map[int]*memPacketConn),
	}
}

// handle returns a new Network view of the fabric.
func (f *memFabric) handle() *memNetwork {
	return &memNetwork{fab: f, owned: make(map[interface{ Close() error }]struct{})}
}

// portInUseLocked reports whether p is bound on either namespace. Callers hold mu.
func (f *memFabric) portInUseLocked(p int) bool {
	_, l := f.listeners[p]
	_, c := f.pconns[p]
	return l || c
}

// allocPortLocked returns an unbound auto-assigned port, scanning the dynamic
// range (49152–65535) monotonically with wraparound — ports freed on close are
// reused, and an allocated port never exceeds 65535, so every address the
// fabric prints round-trips through parsePort. Callers hold mu.
func (f *memFabric) allocPortLocked() (int, error) {
	for i := 0; i < 65536-memFirstPort; i++ {
		f.nextPort++
		if f.nextPort > 65535 || f.nextPort < memFirstPort {
			f.nextPort = memFirstPort
		}
		if !f.portInUseLocked(f.nextPort) {
			return f.nextPort, nil
		}
	}
	return 0, errPortsExhausted
}

// parsePort extracts the numeric port from a host:port address; the host part
// is ignored (fabric routing is by port only). Empty address or port means
// port 0 (auto-assign on bind).
func parsePort(address string) (int, error) {
	if address == "" {
		return 0, nil
	}
	_, ps, err := net.SplitHostPort(address)
	if err != nil {
		return 0, &net.AddrError{Err: err.Error(), Addr: address}
	}
	if ps == "" {
		return 0, nil
	}
	p, err := strconv.Atoi(ps)
	if err != nil || p < 0 || p > 65535 {
		return 0, &net.AddrError{Err: "invalid port (memory addresses are numeric)", Addr: address}
	}
	return p, nil
}

// portOf extracts the destination port from a net.Addr, accepting the fabric's
// own addresses or anything whose String() is host:port.
func portOf(addr net.Addr) (int, error) {
	switch a := addr.(type) {
	case memAddr:
		return a.port, nil
	case *net.UDPAddr:
		return a.Port, nil
	case *net.TCPAddr:
		return a.Port, nil
	}
	return parsePort(addr.String())
}

// memNetwork is one handle of a Memory pair. It tracks the listeners and
// packet conns created through it so Close tears them down (unblocking any
// pending Accept/ReadFrom).
type memNetwork struct {
	fab    *memFabric
	mu     sync.Mutex
	closed bool
	owned  map[interface{ Close() error }]struct{}
}

// Name implements Network.
func (*memNetwork) Name() string { return "memory" }

// DialContext implements Network. "tcp" rendezvouses with a fabric listener
// (net.Pipe streams); "udp" returns a connected datagram conn whether or not
// the remote port is bound yet, like the kernel.
func (m *memNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if err := m.ok(); err != nil {
		return nil, err
	}
	switch {
	case isStream(network):
		return m.fab.dialStream(ctx, address)
	case isPacket(network):
		return m.dialPacket(address)
	}
	return nil, &UnsupportedNetworkError{Impl: "memory", Network: network}
}

// ListenPacket implements Network.
func (m *memNetwork) ListenPacket(network, address string) (net.PacketConn, error) {
	if !isPacket(network) {
		return nil, &UnsupportedNetworkError{Impl: "memory", Network: network}
	}
	if err := m.ok(); err != nil {
		return nil, err
	}
	return m.bindPacket(address)
}

// Listen implements Network.
func (m *memNetwork) Listen(network, address string) (net.Listener, error) {
	if !isStream(network) {
		return nil, &UnsupportedNetworkError{Impl: "memory", Network: network}
	}
	if err := m.ok(); err != nil {
		return nil, err
	}
	port, err := parsePort(address)
	if err != nil {
		return nil, err
	}
	f := m.fab
	f.mu.Lock()
	if port == 0 {
		p, aerr := f.allocPortLocked()
		if aerr != nil {
			f.mu.Unlock()
			return nil, &net.OpError{Op: "listen", Net: "tcp", Addr: memAddr{"tcp", 0}, Err: aerr}
		}
		port = p
	} else if _, dup := f.listeners[port]; dup {
		f.mu.Unlock()
		return nil, &net.OpError{Op: "listen", Net: "tcp", Addr: memAddr{"tcp", port}, Err: errAddrInUse}
	}
	l := &memListener{
		addr: memAddr{"tcp", port},
		ch:   make(chan net.Conn, memChanDepth),
		done: make(chan struct{}),
	}
	l.onClose = func() {
		f.mu.Lock()
		delete(f.listeners, port)
		f.mu.Unlock()
		m.disown(l)
	}
	f.listeners[port] = l
	f.mu.Unlock()
	m.adopt(l)
	return l, nil
}

// Close implements Network: it closes every listener and packet conn created
// through this handle (unblocking pending Accept/ReadFrom calls) and fails
// subsequent dials/listens. Established stream conns and the peer handle are
// unaffected.
func (m *memNetwork) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	owned := make([]interface{ Close() error }, 0, len(m.owned))
	for c := range m.owned {
		owned = append(owned, c)
	}
	m.mu.Unlock()
	for _, c := range owned {
		_ = c.Close()
	}
	return nil
}

// ok fails fast once the handle is closed.
func (m *memNetwork) ok() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return net.ErrClosed
	}
	return nil
}

// adopt records a resource for teardown on Close. If the handle closed while
// the resource was being created, the resource is closed instead of leaked.
func (m *memNetwork) adopt(c interface{ Close() error }) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = c.Close()
		return
	}
	m.owned[c] = struct{}{}
	m.mu.Unlock()
}

// disown forgets a resource that closed itself.
func (m *memNetwork) disown(c interface{ Close() error }) {
	m.mu.Lock()
	delete(m.owned, c)
	m.mu.Unlock()
}

// bindPacket registers a datagram endpoint on the fabric.
func (m *memNetwork) bindPacket(address string) (*memPacketConn, error) {
	port, err := parsePort(address)
	if err != nil {
		return nil, err
	}
	f := m.fab
	f.mu.Lock()
	if port == 0 {
		p, aerr := f.allocPortLocked()
		if aerr != nil {
			f.mu.Unlock()
			return nil, &net.OpError{Op: "listen", Net: "udp", Addr: memAddr{"udp", 0}, Err: aerr}
		}
		port = p
	} else if _, dup := f.pconns[port]; dup {
		f.mu.Unlock()
		return nil, &net.OpError{Op: "listen", Net: "udp", Addr: memAddr{"udp", port}, Err: errAddrInUse}
	}
	c := &memPacketConn{
		fab:       f,
		addr:      memAddr{"udp", port},
		ch:        make(chan memDatagram, memChanDepth),
		done:      make(chan struct{}),
		dlChanged: make(chan struct{}),
	}
	c.onClose = func() {
		f.mu.Lock()
		delete(f.pconns, port)
		f.mu.Unlock()
		m.disown(c)
	}
	f.pconns[port] = c
	f.mu.Unlock()
	m.adopt(c)
	return c, nil
}

// dialPacket binds an ephemeral datagram endpoint connected to address.
func (m *memNetwork) dialPacket(address string) (net.Conn, error) {
	port, err := parsePort(address)
	if err != nil {
		return nil, err
	}
	pc, err := m.bindPacket("")
	if err != nil {
		return nil, err
	}
	return &memDgramConn{memPacketConn: pc, remote: memAddr{"udp", port}}, nil
}

// dialStream rendezvouses with the listener bound at address's port.
func (f *memFabric) dialStream(ctx context.Context, address string) (net.Conn, error) {
	port, err := parsePort(address)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	l := f.listeners[port]
	lp, aerr := f.allocPortLocked() // label only; never registered
	f.mu.Unlock()
	if aerr != nil {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Addr: memAddr{"tcp", port}, Err: aerr}
	}
	laddr := memAddr{"tcp", lp}
	if l == nil {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Addr: memAddr{"tcp", port}, Err: errConnRefused}
	}
	c1, c2 := net.Pipe()
	dc := memConn{Conn: c1, local: laddr, remote: l.addr}
	sc := memConn{Conn: c2, local: l.addr, remote: laddr}
	select {
	case l.ch <- sc:
		return dc, nil
	case <-l.done:
		return nil, &net.OpError{Op: "dial", Net: "tcp", Addr: l.addr, Err: errConnRefused}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// memConn is a net.Pipe stream relabeled with fabric addresses.
type memConn struct {
	net.Conn
	local, remote net.Addr
}

// LocalAddr implements net.Conn.
func (c memConn) LocalAddr() net.Addr { return c.local }

// RemoteAddr implements net.Conn.
func (c memConn) RemoteAddr() net.Addr { return c.remote }

// memListener accepts fabric stream rendezvous.
type memListener struct {
	addr    memAddr
	ch      chan net.Conn
	done    chan struct{}
	once    sync.Once
	onClose func()
}

// Accept implements net.Listener.
func (l *memListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, &net.OpError{Op: "accept", Net: "tcp", Addr: l.addr, Err: net.ErrClosed}
	}
}

// Close implements net.Listener; it unblocks pending Accepts.
func (l *memListener) Close() error {
	l.once.Do(func() {
		close(l.done)
		l.onClose()
	})
	return nil
}

// Addr implements net.Listener.
func (l *memListener) Addr() net.Addr { return l.addr }

// memDatagram is one queued datagram with its sender.
type memDatagram struct {
	data []byte
	from memAddr
}

// memPacketConn is a fabric datagram endpoint.
type memPacketConn struct {
	fab     *memFabric
	addr    memAddr
	ch      chan memDatagram
	done    chan struct{}
	once    sync.Once
	onClose func()

	mu            sync.Mutex
	readDeadline  time.Time
	dlChanged     chan struct{} // closed+replaced by SetReadDeadline to wake blocked reads
	writeDeadline time.Time     // tracked for interface fidelity; writes never block
}

// ReadFrom implements net.PacketConn. A datagram longer than p is truncated,
// as with UDP sockets. The read deadline follows the net.Conn contract: a
// deadline set while this read is blocked (the standard
// SetReadDeadline(time.Now()) interrupt idiom) wakes it to re-arm.
func (c *memPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		c.mu.Lock()
		dl, changed := c.readDeadline, c.dlChanged
		c.mu.Unlock()
		var timer *time.Timer
		var timeout <-chan time.Time
		if !dl.IsZero() {
			d := time.Until(dl)
			if d <= 0 {
				return 0, nil, c.opErr("read", os.ErrDeadlineExceeded)
			}
			timer = time.NewTimer(d)
			timeout = timer.C
		}
		select {
		case dg := <-c.ch:
			if timer != nil {
				timer.Stop()
			}
			return copy(p, dg.data), dg.from, nil
		case <-c.done:
			if timer != nil {
				timer.Stop()
			}
			return 0, nil, c.opErr("read", net.ErrClosed)
		case <-timeout:
			return 0, nil, c.opErr("read", os.ErrDeadlineExceeded)
		case <-changed:
			if timer != nil {
				timer.Stop()
			}
			// Deadline replaced mid-read: loop and re-arm with the new one.
		}
	}
}

// WriteTo implements net.PacketConn. A send to an unbound port succeeds and
// vanishes; a full receiver drops the datagram (UDP semantics).
func (c *memPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	select {
	case <-c.done:
		return 0, c.opErr("write", net.ErrClosed)
	default:
	}
	port, err := portOf(addr)
	if err != nil {
		return 0, err
	}
	c.fab.mu.Lock()
	dst := c.fab.pconns[port]
	c.fab.mu.Unlock()
	if dst == nil {
		return len(p), nil
	}
	dg := memDatagram{data: append([]byte(nil), p...), from: c.addr}
	select {
	case dst.ch <- dg:
	case <-dst.done:
	default:
	}
	return len(p), nil
}

// Close implements net.PacketConn; it unblocks pending ReadFroms.
func (c *memPacketConn) Close() error {
	c.once.Do(func() {
		close(c.done)
		c.onClose()
	})
	return nil
}

// LocalAddr implements net.PacketConn.
func (c *memPacketConn) LocalAddr() net.Addr { return c.addr }

// SetDeadline implements net.PacketConn.
func (c *memPacketConn) SetDeadline(t time.Time) error {
	_ = c.SetReadDeadline(t)
	return c.SetWriteDeadline(t)
}

// SetReadDeadline implements net.PacketConn. Per the net.Conn contract it
// applies to future reads and wakes any currently-blocked ReadFrom so it
// re-arms against the new deadline.
func (c *memPacketConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadline = t
	close(c.dlChanged) // wake blocked reads to re-arm
	c.dlChanged = make(chan struct{})
	c.mu.Unlock()
	return nil
}

// SetWriteDeadline implements net.PacketConn. Writes never block, so this only
// records the deadline.
func (c *memPacketConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.writeDeadline = t
	c.mu.Unlock()
	return nil
}

// opErr wraps err in a net.OpError so callers get standard Timeout()/Is
// behavior.
func (c *memPacketConn) opErr(op string, err error) error {
	return &net.OpError{Op: op, Net: c.addr.net, Addr: c.addr, Err: err}
}

// memDgramConn is a connected view of a memPacketConn (DialContext "udp"):
// Write sends to the fixed remote, Read filters to datagrams from it, dropping
// others as a connected UDP socket does.
type memDgramConn struct {
	*memPacketConn
	remote memAddr
}

// Read implements net.Conn.
func (c *memDgramConn) Read(p []byte) (int, error) {
	for {
		n, from, err := c.ReadFrom(p)
		if err != nil {
			return n, err
		}
		if port, perr := portOf(from); perr == nil && port == c.remote.port {
			return n, nil
		}
	}
}

// Write implements net.Conn.
func (c *memDgramConn) Write(p []byte) (int, error) { return c.WriteTo(p, c.remote) }

// RemoteAddr implements net.Conn.
func (c *memDgramConn) RemoteAddr() net.Addr { return c.remote }
