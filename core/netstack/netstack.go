// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

//go:build !loom_nonetstack

package netstack

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"

	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/netpath"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	gstack "gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

// nicID is the Stack's single NIC: one dpEndpoint carries every address.
const nicID tcpip.NICID = 1

// Stack is one userspace TCP/IP stack over a raw-L3 datapath pair, hosting
// many local addresses (one Stack per gNB in orbit's shape — never one per
// UE). Per-address isolation comes from source-bound Network views.
//
// The Stack owns tx and rx: Close closes both.
type Stack struct {
	st *gstack.Stack
	ep *dpEndpoint
	tx datapath.TxDatapath
	rx datapath.RxDatapath

	mu     sync.Mutex
	closed bool
	addrs  map[netip.Addr]struct{}
	views  map[*view]struct{}
}

// New builds a Stack sending through tx and receiving from rx. Both datapaths
// must advertise Capabilities.RawL3 — their frames are complete IP packets —
// or New refuses them. The Stack takes ownership of tx and rx: its Close
// closes both (on error the caller keeps ownership).
//
// Invariant the caller owes New: tx's frames must hold a full-MTU packet
// (frame size >= Config.MTU). MSS derives from the MTU, so a smaller frame
// makes every full-size segment fail WritePackets with ErrNoBufferSpace and
// TCP retransmits it forever — a silent stall, not an error. FromOptions
// enforces this for registry-built stacks; embedders constructing datapaths
// directly must size their frames accordingly.
func New(cfg Config, tx datapath.TxDatapath, rx datapath.RxDatapath) (*Stack, error) {
	if tx == nil || rx == nil {
		return nil, errors.New("netstack: nil datapath")
	}
	if !tx.Caps().RawL3 {
		return nil, fmt.Errorf("netstack: tx datapath %q lacks Capabilities.RawL3 (netstack requires frames that are complete IP packets)", tx.Name())
	}
	if !rx.Caps().RawL3 {
		return nil, fmt.Errorf("netstack: rx datapath %q lacks Capabilities.RawL3 (netstack requires frames that are complete IP packets)", rx.Name())
	}
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}

	st := gstack.New(gstack.Options{
		NetworkProtocols:   []gstack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []gstack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	fail := func(op string, terr tcpip.Error) (*Stack, error) {
		st.Destroy()
		return nil, fmt.Errorf("netstack: %s: %s", op, terr)
	}
	// SACK + RACK always on; congestion control per Config (cubic default).
	sack := tcpip.TCPSACKEnabled(true)
	if terr := st.SetTransportProtocolOption(tcp.ProtocolNumber, &sack); terr != nil {
		return fail("enable SACK", terr)
	}
	rack := tcpip.TCPRecovery(tcpip.TCPRACKLossDetection)
	if terr := st.SetTransportProtocolOption(tcp.ProtocolNumber, &rack); terr != nil {
		return fail("enable RACK", terr)
	}
	cc := tcpip.CongestionControlOption(cfg.CongestionControl)
	if terr := st.SetTransportProtocolOption(tcp.ProtocolNumber, &cc); terr != nil {
		return fail(fmt.Sprintf("set congestion control %q", cfg.CongestionControl), terr)
	}

	ep := newDPEndpoint(tx, rx, uint32(cfg.MTU))
	if terr := st.CreateNIC(nicID, ep); terr != nil {
		return fail("create NIC", terr)
	}
	// Everything routes out the single NIC; there is no gateway at L3 — the
	// datapath is the "wire".
	st.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})

	return &Stack{
		st:    st,
		ep:    ep,
		tx:    tx,
		rx:    rx,
		addrs: make(map[netip.Addr]struct{}),
		views: make(map[*view]struct{}),
	}, nil
}

// AddAddress adds a local address to the stack (an endpoint attach — e.g. a
// UE getting its session address). IPv4 only for now: IPv6 addresses are
// rejected until IPv6 is wired end to end.
func (s *Stack) AddAddress(a netip.Addr) error {
	a = a.Unmap()
	if !a.Is4() {
		return fmt.Errorf("netstack: address %s is not IPv4 (IPv6 is not wired yet)", a)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return net.ErrClosed
	}
	if _, dup := s.addrs[a]; dup {
		return fmt.Errorf("netstack: address %s already added", a)
	}
	pa := tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddrFrom4(a.As4()).WithPrefix(),
	}
	if terr := s.st.AddProtocolAddress(nicID, pa, gstack.AddressProperties{}); terr != nil {
		return fmt.Errorf("netstack: add address %s: %s", a, terr)
	}
	s.addrs[a] = struct{}{}
	return nil
}

// RemoveAddress removes a local address (an endpoint release). The Stack
// closes every conn and listener created through views of the address, so
// their goroutines and gVisor endpoints are released immediately — gVisor
// itself only expires the address (PermanentExpired), which would leave
// established connections as zombies retransmitting into ErrInvalidEndpointState
// until RTO exhaustion. Other addresses' traffic is unaffected. New dials and
// listens on views of the address fail afterwards; the views themselves stay
// usable if the address is later re-added.
func (s *Stack) RemoveAddress(a netip.Addr) error {
	a = a.Unmap()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return net.ErrClosed
	}
	if _, ok := s.addrs[a]; !ok {
		s.mu.Unlock()
		return fmt.Errorf("netstack: address %s not on the stack", a)
	}
	if terr := s.st.RemoveAddress(nicID, tcpip.AddrFrom4(a.As4())); terr != nil {
		s.mu.Unlock()
		return fmt.Errorf("netstack: remove address %s: %s", a, terr)
	}
	delete(s.addrs, a)
	views := make([]*view, 0, len(s.views))
	for v := range s.views {
		if v.local == a {
			views = append(views, v)
		}
	}
	s.mu.Unlock()
	// Abort outside s.mu: closing conns re-enters gVisor.
	for _, v := range views {
		v.abortConns()
	}
	return nil
}

// Stats is the Stack's datapath-edge counter snapshot.
type Stats struct {
	// RxDroppedNonIP counts inbound frames dropped because they were neither
	// IPv4 nor IPv6 (the doc.go "dropped and counted" contract) — e.g.
	// mis-stripped tunnel headers feeding garbage into the stack.
	RxDroppedNonIP uint64
}

// Stats reports the Stack's datapath-edge counters.
func (s *Stack) Stats() Stats {
	return Stats{RxDroppedNonIP: s.ep.dropNonIP.Load()}
}

// Network returns a source-bound netpath.Network view of the stack:
// DialContext binds local as the connection's source address, and
// Listen/ListenPacket bind on it. The address must have been added with
// AddAddress — views of an unknown (or since-removed) address fail their
// dials and listens with a "not on the stack" error. Closing a view closes
// only the conns and listeners created through it, never the Stack.
func (s *Stack) Network(local netip.Addr) netpath.Network {
	v := &view{s: s, local: local.Unmap(), conns: make(map[*trackedCloser]struct{})}
	s.mu.Lock()
	if s.closed {
		v.closed = true
	} else {
		s.views[v] = struct{}{}
	}
	s.mu.Unlock()
	return v
}

// hasAddr reports whether a is currently on the stack.
func (s *Stack) hasAddr(a netip.Addr) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.addrs[a]
	return ok
}

// dropView unregisters a closed view.
func (s *Stack) dropView(v *view) {
	s.mu.Lock()
	delete(s.views, v)
	s.mu.Unlock()
}

// Close tears the whole stack down deterministically: every view's conns and
// listeners, the gVisor stack and its worker goroutines (waited on, not
// abandoned), and the two datapaths the Stack owns. Close is idempotent.
func (s *Stack) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	views := make([]*view, 0, len(s.views))
	for v := range s.views {
		views = append(views, v)
	}
	s.views = nil
	s.mu.Unlock()

	for _, v := range views {
		v.closeConns()
	}
	// Close rx first: it unblocks a blocking RxPoll so the receive goroutine
	// exits promptly instead of waiting out a poll window.
	errRx := s.rx.Close()
	// Stop the receive goroutine BEFORE Destroy. Destroy's Wait phase holds
	// stack.mu (write) while it waits for the link endpoint's goroutine in
	// Attach(nil); if that goroutine is mid-delivery and needs stack.mu (read)
	// — e.g. FindRoute for an RST to an in-flight segment whose endpoint
	// Destroy just aborted — teardown under traffic deadlocks permanently.
	// With the loop already stopped, Destroy has nothing to wait for.
	s.ep.stopRx()
	// Destroy aborts every transport endpoint, waits for all worker
	// goroutines, removes the NIC, and closes the link endpoint. After it
	// returns no more WritePackets are in flight, so closing tx cannot race an
	// in-progress TxReserve/TxCommit pair.
	s.st.Destroy()
	errTx := s.tx.Close()
	return errors.Join(errTx, errRx)
}
