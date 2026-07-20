// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

//go:build !loom_nonetstack

package netstack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"

	"github.com/bgrewell/loom/core/netpath"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	gstack "gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// view is the source-bound netpath.Network a Stack returns from Network:
// every dial, listen, and packet bind uses the view's local address. It
// tracks what it creates so Close tears down exactly its own conns and
// listeners — never the Stack's other views, addresses, or the Stack itself.
type view struct {
	s     *Stack
	local netip.Addr

	mu     sync.Mutex
	closed bool
	conns  map[*trackedCloser]struct{}
}

var _ netpath.Network = (*view)(nil)

// Name implements netpath.Network.
func (*view) Name() string { return "netstack" }

// check gates every dial/listen: the view must be open and its local address
// must currently be on the stack (AddAddress'd and not since removed).
func (v *view) check() error {
	v.mu.Lock()
	closed := v.closed
	v.mu.Unlock()
	if closed {
		return net.ErrClosed
	}
	if !v.s.hasAddr(v.local) {
		return fmt.Errorf("netstack: local address %s not on the stack (AddAddress it first)", v.local)
	}
	return nil
}

// DialContext implements netpath.Network: "tcp"/"tcp4" dials bind the view's
// local address as the connection source (gonet.DialTCPWithBind), and
// "udp"/"udp4" dials return a connected UDP conn bound to it. IPv6 network
// names are rejected until IPv6 is wired.
func (v *view) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "udp", "udp4":
	default:
		return nil, &netpath.UnsupportedNetworkError{Impl: "netstack", Network: network}
	}
	if err := v.check(); err != nil {
		return nil, err
	}
	remote, err := parseIPv4Target(address)
	if err != nil {
		return nil, err
	}
	switch network {
	case "tcp", "tcp4":
		c, err := dialTCP(ctx, v.s.st,
			tcpip.FullAddress{NIC: nicID, Addr: addr4(v.local)},
			fullAddr(remote))
		if err != nil {
			return nil, err
		}
		t, terr := v.track(c)
		if terr != nil {
			return nil, terr
		}
		return &trackedConn{Conn: c, t: t}, nil
	case "udp", "udp4":
		laddr := tcpip.FullAddress{NIC: nicID, Addr: addr4(v.local)}
		raddr := fullAddr(remote)
		c, err := gonet.DialUDP(v.s.st, &laddr, &raddr, ipv4.ProtocolNumber)
		if err != nil {
			return nil, err
		}
		t, terr := v.track(c)
		if terr != nil {
			return nil, terr
		}
		return &trackedConn{Conn: c, t: t}, nil
	default: // unreachable: filtered above
		return nil, &netpath.UnsupportedNetworkError{Impl: "netstack", Network: network}
	}
}

// dialTCP is gonet.DialTCPWithBind with the endpoint leak fixed: the pinned
// gonet returns WITHOUT ep.Close() when Bind fails or when ctx is done before
// Connect, permanently leaking a transport endpoint registered in the stack —
// and a dial can race an address removal into exactly that Bind failure, so
// on a long-lived Stack retried dials would accumulate endpoints until
// Stack.Close. Every error path here closes the endpoint.
func dialTCP(ctx context.Context, s *gstack.Stack, laddr, raddr tcpip.FullAddress) (net.Conn, error) {
	var wq waiter.Queue
	ep, terr := s.NewEndpoint(tcp.ProtocolNumber, ipv4.ProtocolNumber, &wq)
	if terr != nil {
		return nil, errors.New(terr.String())
	}
	waitEntry, notifyCh := waiter.NewChannelEntry(waiter.WritableEvents)
	wq.EventRegister(&waitEntry)
	defer wq.EventUnregister(&waitEntry)

	if err := ctx.Err(); err != nil {
		ep.Close()
		return nil, err
	}
	if terr := ep.Bind(laddr); terr != nil {
		ep.Close()
		return nil, fmt.Errorf("ep.Bind(%+v) = %s", laddr, terr)
	}
	terr = ep.Connect(raddr)
	if _, ok := terr.(*tcpip.ErrConnectStarted); ok {
		select {
		case <-ctx.Done():
			ep.Close()
			return nil, ctx.Err()
		case <-notifyCh:
		}
		terr = ep.LastError()
	}
	if terr != nil {
		ep.Close()
		return nil, &net.OpError{
			Op:  "connect",
			Net: "tcp",
			Addr: &net.TCPAddr{
				IP:   net.IP(raddr.Addr.AsSlice()),
				Port: int(raddr.Port),
			},
			Err: errors.New(terr.String()),
		}
	}
	return gonet.NewTCPConn(&wq, ep), nil
}

// ListenPacket implements netpath.Network: a UDP endpoint bound on the view's
// local address. address's host part must be empty, unspecified, or the local
// address; port 0 binds an ephemeral port.
func (v *view) ListenPacket(network, address string) (net.PacketConn, error) {
	switch network {
	case "udp", "udp4":
	default:
		return nil, &netpath.UnsupportedNetworkError{Impl: "netstack", Network: network}
	}
	if err := v.check(); err != nil {
		return nil, err
	}
	port, err := v.parseBind(address)
	if err != nil {
		return nil, err
	}
	laddr := tcpip.FullAddress{NIC: nicID, Addr: addr4(v.local), Port: port}
	c, err := gonet.DialUDP(v.s.st, &laddr, nil, ipv4.ProtocolNumber)
	if err != nil {
		return nil, err
	}
	t, terr := v.track(c)
	if terr != nil {
		return nil, terr
	}
	return &trackedPacketConn{PacketConn: c, t: t}, nil
}

// Listen implements netpath.Network: a TCP listener bound on the view's local
// address. Conns it accepts belong to the view too — view Close closes them.
func (v *view) Listen(network, address string) (net.Listener, error) {
	switch network {
	case "tcp", "tcp4":
	default:
		return nil, &netpath.UnsupportedNetworkError{Impl: "netstack", Network: network}
	}
	if err := v.check(); err != nil {
		return nil, err
	}
	port, err := v.parseBind(address)
	if err != nil {
		return nil, err
	}
	ln, err := gonet.ListenTCP(v.s.st, tcpip.FullAddress{NIC: nicID, Addr: addr4(v.local), Port: port}, ipv4.ProtocolNumber)
	if err != nil {
		return nil, err
	}
	t, terr := v.track(ln)
	if terr != nil {
		return nil, terr
	}
	return &trackedListener{Listener: ln, v: v, t: t}, nil
}

// Close implements netpath.Network: it closes every conn and listener created
// through this view (and the conns its listeners accepted) and detaches the
// view from the Stack. The Stack, its addresses, and every other view stay
// live.
func (v *view) Close() error {
	v.closeConns()
	v.s.dropView(v)
	return nil
}

// closeConns marks the view closed and closes everything it tracks.
func (v *view) closeConns() {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return
	}
	v.closed = true
	conns := make([]*trackedCloser, 0, len(v.conns))
	for c := range v.conns {
		conns = append(conns, c)
	}
	v.conns = nil
	v.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// abortConns closes every conn and listener the view currently tracks
// without closing the view itself: Stack.RemoveAddress uses it so a released
// address's connections are torn down immediately (gVisor only expires the
// address), while the view stays usable should the address be re-added.
func (v *view) abortConns() {
	v.mu.Lock()
	conns := make([]*trackedCloser, 0, len(v.conns))
	for c := range v.conns {
		conns = append(conns, c)
	}
	v.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// track registers c for view-scoped teardown and returns the wrapper that
// deregisters it on Close. If the view was concurrently closed, c is closed
// and net.ErrClosed returned — callers must not hand a dead conn to the user
// with a nil error.
func (v *view) track(c io.Closer) (*trackedCloser, error) {
	t := &trackedCloser{v: v, c: c}
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		_ = c.Close()
		return nil, net.ErrClosed
	}
	v.conns[t] = struct{}{}
	v.mu.Unlock()
	return t, nil
}

// untrack removes t after it closed itself.
func (v *view) untrack(t *trackedCloser) {
	v.mu.Lock()
	if v.conns != nil {
		delete(v.conns, t)
	}
	v.mu.Unlock()
}

// parseBind extracts the port from a bind address, validating that any
// explicit host part is the view's local address (or unspecified). Empty
// address or port means port 0 (ephemeral).
func (v *view) parseBind(address string) (uint16, error) {
	if address == "" {
		return 0, nil
	}
	host, ps, err := net.SplitHostPort(address)
	if err != nil {
		return 0, &net.AddrError{Err: err.Error(), Addr: address}
	}
	if host != "" {
		ip, perr := netip.ParseAddr(host)
		if perr != nil {
			return 0, &net.AddrError{Err: "not an IP literal (netstack does no name resolution)", Addr: address}
		}
		if ip = ip.Unmap(); !ip.IsUnspecified() && ip != v.local {
			return 0, &net.AddrError{Err: "cannot bind: view's local address is " + v.local.String(), Addr: address}
		}
	}
	if ps == "" {
		return 0, nil
	}
	p, err := strconv.Atoi(ps)
	if err != nil || p < 0 || p > 65535 {
		return 0, &net.AddrError{Err: "invalid port (netstack addresses are numeric)", Addr: address}
	}
	return uint16(p), nil
}

// trackedCloser ties one created conn/listener to its view: Close closes the
// underlying object once and deregisters it, whether called by the user or by
// the view's teardown.
type trackedCloser struct {
	v    *view
	c    io.Closer
	once sync.Once
	err  error
}

// Close implements io.Closer.
func (t *trackedCloser) Close() error {
	t.once.Do(func() {
		t.v.untrack(t)
		t.err = t.c.Close()
	})
	return t.err
}

// trackedConn is a net.Conn whose Close deregisters it from its view.
type trackedConn struct {
	net.Conn
	t *trackedCloser
}

// Close implements net.Conn.
func (c *trackedConn) Close() error { return c.t.Close() }

// trackedPacketConn is a net.PacketConn whose Close deregisters it.
type trackedPacketConn struct {
	net.PacketConn
	t *trackedCloser
}

// Close implements net.PacketConn.
func (c *trackedPacketConn) Close() error { return c.t.Close() }

// trackedListener wraps a listener so Close deregisters it and every
// accepted conn is tracked by the view as well.
type trackedListener struct {
	net.Listener
	v *view
	t *trackedCloser
}

// Accept implements net.Listener: a conn accepted while the view is being
// closed is closed and net.ErrClosed returned, per net.Listener semantics —
// never an already-dead conn with a nil error.
func (l *trackedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	t, terr := l.v.track(c)
	if terr != nil {
		return nil, terr
	}
	return &trackedConn{Conn: c, t: t}, nil
}

// Close implements net.Listener.
func (l *trackedListener) Close() error { return l.t.Close() }

// addr4 converts a (validated IPv4) netip.Addr to a tcpip.Address.
func addr4(a netip.Addr) tcpip.Address { return tcpip.AddrFrom4(a.As4()) }

// fullAddr converts an IPv4 netip.AddrPort to a tcpip.FullAddress on the NIC.
func fullAddr(ap netip.AddrPort) tcpip.FullAddress {
	return tcpip.FullAddress{NIC: nicID, Addr: addr4(ap.Addr()), Port: ap.Port()}
}

// parseIPv4Target parses an ip:port literal and requires an IPv4 (or
// 4-in-6-mapped) address.
func parseIPv4Target(address string) (netip.AddrPort, error) {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return netip.AddrPort{}, &net.AddrError{Err: "not an ip:port literal (netstack does no name resolution)", Addr: address}
	}
	a := ap.Addr().Unmap()
	if !a.Is4() {
		return netip.AddrPort{}, &net.AddrError{Err: "netstack is IPv4-only for now", Addr: address}
	}
	return netip.AddrPortFrom(a, ap.Port()), nil
}
