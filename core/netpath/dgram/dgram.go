// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package dgram is a UDP-only netpath.Network that encodes real IPv4+UDP
// headers into the frames of a raw-L3 datapath (datapath.Capabilities.RawL3):
// every WriteTo emits a complete, checksum-correct IP packet, and inbound
// frames are validated, demultiplexed by destination port, and delivered to
// the bound net.PacketConn. It generalizes "UDP over an injected packet lane"
// (e.g. a GTP-U inner-IP path) behind the standard netpath seam.
//
// Scope and semantics:
//
//   - IPv4 only. The local address must be IPv4 and "udp6" (or any IPv6
//     address) is rejected; IPv6 arrives with a later revision. Accepted
//     network names are "udp" and "udp4".
//   - UDP only. DialContext/Listen with "tcp" (or anything else) return the
//     netpath.ErrUnsupportedNetwork sentinel; stream networks additionally
//     match the ErrTCPUnsupported sentinel so callers can distinguish
//     "UDP-only network, pick a TCP-capable backend" from an unknown network
//     name. Listen supports no network at all. A userspace TCP stack is a
//     different netpath backend.
//   - Addresses are numeric ip:port literals; there is no name resolution.
//   - Outbound checksums are always computed — the IPv4 header checksum and
//     the UDP checksum over its pseudo-header (a computed zero is sent as
//     0xFFFF per RFC 768), so captures are Wireshark-clean. Inbound packets
//     with a zero UDP checksum are accepted ("not computed" is legal for
//     IPv4); packets that are not IPv4, are fragments, are not UDP, or fail
//     either checksum are silently dropped and counted (see DropStats).
//   - Binding port 0 allocates an ephemeral local port; conns on one Network
//     are demultiplexed by destination port by a single receive goroutine.
//   - Per-frame arrival timestamps (datapath Frame.Meta, ADR-0020) are
//     preserved: every net.PacketConn returned by this package implements
//     MetaConn, whose ReadFromMeta also returns the receive timestamp.
//   - Packet-conn read deadlines follow the net.Conn contract: setting a
//     deadline affects future reads and wakes any currently-blocked
//     ReadFrom/Read with os.ErrDeadlineExceeded. Writes never block.
//
// Construction: embedders with live datapaths (e.g. orbit's GTP-U lanes) call
// New; registry-driven callers use FromOptions, which resolves the datapath
// names in netpath.Options from a components.Components. Importing this
// package also registers the "dgram" factory in netpath.Registry, backed by
// the default component set. The Network owns its datapaths: Close stops the
// receive loop, closes every conn, and closes both datapaths.
package dgram

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bgrewell/loom/core/components"
	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/netpath"
)

const (
	// defaultMTU bounds packets when Options.MTU / New's mtu is unset.
	defaultMTU = 1500
	// maxMTU is the IPv4 maximum total packet length: the header's 16-bit
	// total-length (and UDP's 16-bit length) field cannot describe more, so a
	// larger MTU could only emit corrupt packets.
	maxMTU = 65535
	// firstEphemeralPort is where port-0 binds start (the IANA dynamic range).
	firstEphemeralPort = 49152
	// connBacklog is the per-conn receive queue depth; a full queue drops the
	// datagram and counts it (DropStats.Overflow), UDP-style.
	connBacklog = 256
	// rxBatch is the frame batch size per RxPoll.
	rxBatch = 64
	// pollIdleDelay paces the receive loop when a non-blocking backend (the
	// memory datapath) returns an empty poll, so an idle network doesn't spin.
	pollIdleDelay = 50 * time.Microsecond
)

var errAddrInUse = errors.New("address already in use")

// ErrTCPUnsupported is matched by errors.Is when a stream network ("tcp",
// "tcp4", "tcp6") is requested from this UDP-only Network, so callers routing
// apps across netpath backends can distinguish "this network cannot do TCP —
// pick a TCP-capable backend" from an unknown network name. Errors carrying
// this sentinel also match netpath.ErrUnsupportedNetwork.
var ErrTCPUnsupported = errors.New("tcp unsupported (dgram is UDP-only)")

// errUnsupported builds the refusal for an unsupported network name, adding
// the ErrTCPUnsupported sentinel for stream networks.
func errUnsupported(network string) error {
	err := &netpath.UnsupportedNetworkError{Impl: "dgram", Network: network}
	switch network {
	case "tcp", "tcp4", "tcp6":
		return fmt.Errorf("%w: %w", ErrTCPUnsupported, err)
	}
	return err
}

// DropStats counts inbound frames discarded by a dgram Network. Drops are
// silent on the data path (as UDP is) but always counted, so interference is
// observable. Retrieve a snapshot via the DropReporter interface.
type DropStats struct {
	BadIPHeader    uint64 // short, non-IPv4, bad IHL/length, or IPv4 header checksum mismatch
	Fragmented     uint64 // MF flag or nonzero fragment offset (reassembly unsupported)
	NotUDP         uint64 // IPv4 protocol other than 17
	BadUDPHeader   uint64 // UDP length field short or exceeding the IP payload
	BadUDPChecksum uint64 // nonzero UDP checksum that failed verification
	NoEndpoint     uint64 // destination address not this network's, or port unbound
	Overflow       uint64 // bound conn's receive queue full
}

// DropReporter is implemented by the Networks this package returns; callers
// type-assert to read drop counters.
type DropReporter interface {
	// Drops returns a snapshot of the network's drop counters.
	Drops() DropStats
}

// MetaConn is a net.PacketConn that also exposes per-datagram receive
// metadata. Every packet conn returned by this package implements it, so
// callers that need arrival times (jitter measurement) type-assert to
// MetaConn while everything else uses the plain net.PacketConn.
type MetaConn interface {
	net.PacketConn
	// ReadFromMeta is ReadFrom plus the datagram's receive timestamp, taken
	// from the datapath frame's Meta (ADR-0020) — hardware timestamps flow
	// through unchanged once a backend supplies them. arrival is the zero
	// time.Time when the datapath did not stamp the frame.
	ReadFromMeta(p []byte) (n int, from net.Addr, arrival time.Time, err error)
}

// New builds a dgram Network sending through tx and receiving from rx, using
// local as the IPv4 source address of every packet, with mtu bounding the
// total packet size (0 means 1500; at least the 28 header bytes, at most the
// 65535-byte IPv4 maximum). Both datapaths must advertise
// Capabilities.RawL3 — their frames are complete IP packets — or New refuses
// them. The Network takes ownership of tx and rx: its Close closes both.
func New(tx datapath.TxDatapath, rx datapath.RxDatapath, local netip.Addr, mtu int) (netpath.Network, error) {
	if tx == nil || rx == nil {
		return nil, errors.New("dgram: nil datapath")
	}
	if !tx.Caps().RawL3 {
		return nil, fmt.Errorf("dgram: tx datapath %q lacks Capabilities.RawL3 (dgram requires frames that are complete IP packets)", tx.Name())
	}
	if !rx.Caps().RawL3 {
		return nil, fmt.Errorf("dgram: rx datapath %q lacks Capabilities.RawL3 (dgram requires frames that are complete IP packets)", rx.Name())
	}
	local = local.Unmap()
	if !local.Is4() {
		return nil, fmt.Errorf("dgram: local address %s is not IPv4 (dgram is IPv4-only)", local)
	}
	if mtu <= 0 {
		mtu = defaultMTU
	}
	if mtu < headersLen {
		return nil, fmt.Errorf("dgram: mtu %d below the %d-byte IPv4+UDP headers", mtu, headersLen)
	}
	if mtu > maxMTU {
		return nil, fmt.Errorf("dgram: mtu %d exceeds the %d-byte IPv4 maximum packet size", mtu, maxMTU)
	}
	n := &dgramNetwork{
		tx:            tx,
		rx:            rx,
		local:         local,
		mtu:           mtu,
		conns:         make(map[uint16]*packetConn),
		nextEphemeral: firstEphemeralPort,
		done:          make(chan struct{}),
	}
	go n.rxLoop()
	return n, nil
}

// FromOptions is the registry factory: it resolves o.TxDatapath/o.RxDatapath
// by name from c's datapath registries (nil c means components.Default()),
// builds them with o.DatapathOpts, and hands them to New. When
// DatapathOpts.FrameSize is unset it defaults to the network's MTU so frames
// fit whole packets.
func FromOptions(c *components.Components, o netpath.Options) (netpath.Network, error) {
	c = components.OrDefault(c)
	if o.TxDatapath == "" || o.RxDatapath == "" {
		return nil, errors.New("dgram: Options must name both TxDatapath and RxDatapath")
	}
	mtu := o.MTU
	if mtu <= 0 {
		mtu = defaultMTU
	}
	dpo := o.DatapathOpts
	if dpo.FrameSize == 0 {
		dpo.FrameSize = mtu
	}
	tx, err := c.TxDatapaths.Build(o.TxDatapath, dpo)
	if err != nil {
		return nil, fmt.Errorf("dgram: build tx datapath %q: %w", o.TxDatapath, err)
	}
	rx, err := c.RxDatapaths.Build(o.RxDatapath, dpo)
	if err != nil {
		_ = tx.Close()
		return nil, fmt.Errorf("dgram: build rx datapath %q: %w", o.RxDatapath, err)
	}
	n, err := New(tx, rx, o.Local, mtu)
	if err != nil {
		_ = tx.Close()
		_ = rx.Close()
		return nil, err
	}
	return n, nil
}

func init() {
	// The registry factory is pinned to the DEFAULT component set: Options are
	// pure data (ADR-0006), so a registry entry cannot carry an injected
	// *Components, and datapath names resolve from the global registries.
	// Callers with an injected component set must go through
	// FromOptions(theirComponents, o) — or New with live datapaths — to stay
	// inside their injection boundary (ADR-0022).
	netpath.Registry.Register("dgram", func(o netpath.Options) (netpath.Network, error) {
		return FromOptions(components.Default(), o)
	})
}

// dgramNetwork is the Network implementation: one TX lane serialized by txMu,
// one RX goroutine demultiplexing to conns by destination port.
type dgramNetwork struct {
	tx    datapath.TxDatapath
	rx    datapath.RxDatapath
	local netip.Addr
	mtu   int

	txMu sync.Mutex    // serializes TxReserve/TxCommit pairs
	ipID atomic.Uint32 // IPv4 Identification counter

	mu            sync.Mutex
	closed        bool
	conns         map[uint16]*packetConn // bound local port → conn
	nextEphemeral uint16

	done  chan struct{}
	drops dropCounters
}

var _ netpath.Network = (*dgramNetwork)(nil)
var _ DropReporter = (*dgramNetwork)(nil)

// dropCounters is the atomic backing for DropStats.
type dropCounters struct {
	badIPHeader    atomic.Uint64
	fragmented     atomic.Uint64
	notUDP         atomic.Uint64
	badUDPHeader   atomic.Uint64
	badUDPChecksum atomic.Uint64
	noEndpoint     atomic.Uint64
	overflow       atomic.Uint64
}

// Name implements Network.
func (*dgramNetwork) Name() string { return "dgram" }

// Drops implements DropReporter.
func (n *dgramNetwork) Drops() DropStats {
	return DropStats{
		BadIPHeader:    n.drops.badIPHeader.Load(),
		Fragmented:     n.drops.fragmented.Load(),
		NotUDP:         n.drops.notUDP.Load(),
		BadUDPHeader:   n.drops.badUDPHeader.Load(),
		BadUDPChecksum: n.drops.badUDPChecksum.Load(),
		NoEndpoint:     n.drops.noEndpoint.Load(),
		Overflow:       n.drops.overflow.Load(),
	}
}

// DialContext implements Network: "udp"/"udp4" return a connected wrapper over
// an ephemeral-port packet conn (Write sends to address, Read filters to
// datagrams from it). address must be an IPv4 ip:port literal. The context
// gates only setup — there is no handshake to await.
func (n *dgramNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if !isUDP4(network) {
		return nil, errUnsupported(network)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	remote, err := parseIPv4Target(address)
	if err != nil {
		return nil, err
	}
	pc, err := n.bind(0)
	if err != nil {
		return nil, err
	}
	return &conn{packetConn: pc, remote: remote}, nil
}

// ListenPacket implements Network. address's host part must be empty,
// unspecified, or the network's local address; port 0 binds an ephemeral port.
func (n *dgramNetwork) ListenPacket(network, address string) (net.PacketConn, error) {
	if !isUDP4(network) {
		return nil, errUnsupported(network)
	}
	port, err := n.parseBind(address)
	if err != nil {
		return nil, err
	}
	return n.bind(port)
}

// Listen implements Network. dgram is UDP-only, so every stream network is
// refused with the netpath unsupported-network error; "tcp"/"tcp4"/"tcp6"
// additionally match ErrTCPUnsupported.
func (n *dgramNetwork) Listen(network, address string) (net.Listener, error) {
	return nil, errUnsupported(network)
}

// Close implements Network: it stops the receive loop, closes every conn
// (unblocking pending reads), and closes both datapaths the network owns.
func (n *dgramNetwork) Close() error {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	conns := make([]*packetConn, 0, len(n.conns))
	for _, c := range n.conns {
		conns = append(conns, c)
	}
	n.mu.Unlock()
	close(n.done)
	for _, c := range conns {
		_ = c.Close()
	}
	// rx is closed without a lock on purpose — that is what unblocks a
	// blocking RxPoll — but tx.Close waits for txMu so it cannot land in the
	// middle of an in-flight TxReserve/TxCommit pair (whose frames may alias
	// backend-owned memory, ADR-0019).
	n.txMu.Lock()
	errTx := n.tx.Close()
	n.txMu.Unlock()
	return errors.Join(errTx, n.rx.Close())
}

// isUDP4 reports whether network names a UDP network this IPv4-only
// implementation accepts ("udp6" is not it).
func isUDP4(network string) bool { return network == "udp" || network == "udp4" }

// parseIPv4Target parses an ip:port literal and requires an IPv4 (or
// 4-in-6-mapped) address.
func parseIPv4Target(address string) (netip.AddrPort, error) {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return netip.AddrPort{}, &net.AddrError{Err: "not an ip:port literal (dgram does no name resolution)", Addr: address}
	}
	a := ap.Addr().Unmap()
	if !a.Is4() {
		return netip.AddrPort{}, &net.AddrError{Err: "dgram is IPv4-only", Addr: address}
	}
	return netip.AddrPortFrom(a, ap.Port()), nil
}

// parseBind extracts the port from a bind address, validating that any
// explicit host part is this network's local address (or unspecified). Empty
// address or port means port 0 (ephemeral).
func (n *dgramNetwork) parseBind(address string) (uint16, error) {
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
			return 0, &net.AddrError{Err: "not an IP literal (dgram does no name resolution)", Addr: address}
		}
		if ip = ip.Unmap(); !ip.IsUnspecified() && ip != n.local {
			return 0, &net.AddrError{Err: "cannot bind: network's local address is " + n.local.String(), Addr: address}
		}
	}
	if ps == "" {
		return 0, nil
	}
	p, err := strconv.Atoi(ps)
	if err != nil || p < 0 || p > 65535 {
		return 0, &net.AddrError{Err: "invalid port (dgram addresses are numeric)", Addr: address}
	}
	return uint16(p), nil
}

// bind registers a conn on port (0 = allocate ephemeral).
func (n *dgramNetwork) bind(port uint16) (*packetConn, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil, net.ErrClosed
	}
	if port == 0 {
		p, err := n.allocEphemeralLocked()
		if err != nil {
			return nil, err
		}
		port = p
	} else if _, dup := n.conns[port]; dup {
		return nil, &net.OpError{Op: "listen", Net: "udp", Addr: udpAddr(netip.AddrPortFrom(n.local, port)), Err: errAddrInUse}
	}
	c := &packetConn{
		n:         n,
		port:      port,
		ch:        make(chan inbound, connBacklog),
		done:      make(chan struct{}),
		dlChanged: make(chan struct{}),
	}
	n.conns[port] = c
	return c, nil
}

// allocEphemeralLocked returns an unbound port from the dynamic range,
// scanning monotonically with wraparound. Callers hold mu.
func (n *dgramNetwork) allocEphemeralLocked() (uint16, error) {
	for i := 0; i < 65536-firstEphemeralPort; i++ {
		p := n.nextEphemeral
		if n.nextEphemeral++; n.nextEphemeral < firstEphemeralPort {
			n.nextEphemeral = firstEphemeralPort // uint16 wraparound
		}
		if _, used := n.conns[p]; !used {
			return p, nil
		}
	}
	return 0, errors.New("dgram: ephemeral ports exhausted")
}

// unregister removes a closed conn's port binding.
func (n *dgramNetwork) unregister(port uint16, c *packetConn) {
	n.mu.Lock()
	if n.conns[port] == c {
		delete(n.conns, port)
	}
	n.mu.Unlock()
}

// send encodes one IPv4+UDP packet from local:srcPort to dst into a reserved
// tx frame and commits it. It is the single serialization point for the tx
// datapath (TxReserve/TxCommit pairs must not interleave).
func (n *dgramNetwork) send(srcPort uint16, dst netip.AddrPort, p []byte) error {
	total := headersLen + len(p)
	if total > n.mtu {
		return &net.OpError{Op: "write", Net: "udp", Addr: udpAddr(dst),
			Err: fmt.Errorf("message too long (%d-byte packet, mtu %d)", total, n.mtu)}
	}
	n.txMu.Lock()
	defer n.txMu.Unlock()
	frames := n.tx.TxReserve(1)
	if len(frames) == 0 {
		return &net.OpError{Op: "write", Net: "udp", Addr: udpAddr(dst), Err: errors.New("no tx frames available")}
	}
	f := &frames[0]
	// Bound by len(f.Data) — the frame's usable size — never cap: slab-backed
	// backends (e.g. datapath.Arena) hand out frames whose cap runs to the end
	// of the shared slab, so growing to cap would write into neighboring
	// frames' memory.
	if len(f.Data) < total {
		_, _ = n.tx.TxCommit(frames[:0]) // release without sending
		return &net.OpError{Op: "write", Net: "udp", Addr: udpAddr(dst),
			Err: fmt.Errorf("datapath frame size %d below %d-byte packet", len(f.Data), total)}
	}
	f.Len = encodePacket(f.Data, netip.AddrPortFrom(n.local, srcPort), dst, uint16(n.ipID.Add(1)), p)
	sent, err := n.tx.TxCommit(frames[:1])
	if err != nil {
		return &net.OpError{Op: "write", Net: "udp", Addr: udpAddr(dst), Err: err}
	}
	if sent != 1 {
		return &net.OpError{Op: "write", Net: "udp", Addr: udpAddr(dst), Err: errors.New("datapath refused the frame")}
	}
	return nil
}

// rxLoop is the network's single receive goroutine: poll, validate/deliver,
// release, until Close. Blocking backends bound their own poll (returning a
// timeout net.Error); the non-blocking memory backend returns empty polls,
// paced by pollIdleDelay.
func (n *dgramNetwork) rxLoop() {
	for {
		select {
		case <-n.done:
			return
		default:
		}
		frames, err := n.rx.RxPoll(rxBatch)
		if len(frames) > 0 {
			for i := range frames {
				n.deliver(&frames[i])
			}
			n.rx.RxRelease(frames)
		}
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue // poll window elapsed; recheck done
			}
			select {
			case <-n.done:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(pollIdleDelay) // unexpected error: don't spin
			continue
		}
		if len(frames) == 0 {
			time.Sleep(pollIdleDelay)
		}
	}
}

// deliver validates one frame and queues its payload on the conn bound to the
// destination port, counting every discard. The payload is copied before the
// frame goes back to the datapath (borrow contract, ADR-0019).
func (n *dgramNetwork) deliver(f *datapath.Frame) {
	data := f.Data
	if f.Len < len(data) {
		data = data[:f.Len]
	}
	src, dst, payload, reason := parsePacket(data)
	switch reason {
	case dropNone:
	case dropFragmented:
		n.drops.fragmented.Add(1)
		return
	case dropNotUDP:
		n.drops.notUDP.Add(1)
		return
	case dropBadUDPHeader:
		n.drops.badUDPHeader.Add(1)
		return
	case dropBadUDPChecksum:
		n.drops.badUDPChecksum.Add(1)
		return
	default:
		n.drops.badIPHeader.Add(1)
		return
	}
	if dst.Addr() != n.local {
		n.drops.noEndpoint.Add(1)
		return
	}
	n.mu.Lock()
	c := n.conns[dst.Port()]
	n.mu.Unlock()
	if c == nil {
		n.drops.noEndpoint.Add(1)
		return
	}
	in := inbound{payload: append([]byte(nil), payload...), src: src}
	if f.Meta.Nanos != 0 {
		in.arrival = time.Unix(0, f.Meta.Nanos)
	}
	select {
	case c.ch <- in:
	case <-c.done:
	default:
		n.drops.overflow.Add(1)
	}
}

// udpAddr converts a netip.AddrPort to *net.UDPAddr for net.Addr surfaces.
func udpAddr(ap netip.AddrPort) *net.UDPAddr {
	return &net.UDPAddr{IP: ap.Addr().AsSlice(), Port: int(ap.Port())}
}
