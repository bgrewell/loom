// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package emul

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/bgrewell/loom/core/accounting"
	"github.com/bgrewell/loom/core/netpath"
)

// Request/response emulations (e.g. https-browse, ftp-transfer) need real
// bidirectional traffic: a client requests an object and a server returns it, so
// the *download* bytes flow server→client. That is connection-oriented and two-
// way — unlike the one-directional frame datapath — so the requester and
// responder here dial and listen through an injected netpath.Network (TCP or
// UDP). The default is the kernel stack (netpath.Host), but any Network works:
// the same session runs over the in-memory fabric in tests, or a
// datapath-backed network in an embedder — no concrete net.Dial/net.Listen.
//
// Wire protocol: a request is an 8-byte big-endian response size; the response
// is that many bytes. One generic responder serves any request/response
// emulation — the size knobs live on the client script.

const (
	reqHeaderLen = 8
	reqIOTimeout = 30 * time.Second // bounds a single request's read
)

// Requester drives a BehaviorScript as request/response: for each step it asks
// the responder for Step.Size bytes, reads (and accounts) the response, then
// waits Step.Think. It implements flow.Runner.
type Requester struct {
	script BehaviorScript
	conn   net.Conn
	after  time.Duration
	count  uint64
	volume uint64
	rng    *rand.Rand
	hdr    [reqHeaderLen]byte
	buf    []byte
	acct   accounting.Counters
}

// DialRequester connects to a responder at target over transport ("tcp"|"udp")
// on the kernel stack and prepares a request/response runner.
//
// Deprecated: use NewRequester with an injected netpath.Network. DialRequester
// is a back-compat wrapper pinned to netpath.Host, so its sessions cannot ride
// a datapath-backed or in-memory network.
func DialRequester(transport, target string, script BehaviorScript, mtu int, after time.Duration, count, volume uint64, seed int64) (*Requester, error) {
	return NewRequester(context.Background(), netpath.Host(netip.Addr{}), transport, target, script, mtu, after, count, volume, seed)
}

// NewRequester connects to a responder at target over transport ("tcp"|"udp")
// through n and prepares a request/response runner. ctx bounds the dial only.
// The caller retains ownership of n; the Requester owns just the connection it
// dialed (released by Close/Run).
func NewRequester(ctx context.Context, n netpath.Network, transport, target string, script BehaviorScript, mtu int, after time.Duration, count, volume uint64, seed int64) (*Requester, error) {
	switch transport {
	case "tcp", "udp":
	default:
		return nil, fmt.Errorf("request/response transport must be tcp or udp, got %q", transport)
	}
	c, err := n.DialContext(ctx, transport, target)
	if err != nil {
		return nil, err
	}
	if mtu < 1 {
		mtu = 32 * 1024
	}
	return &Requester{
		script: script, conn: c,
		after: after, count: count, volume: volume,
		rng: rand.New(rand.NewSource(seed)), buf: make([]byte, mtu),
	}, nil
}

// Counters exposes the bytes/transactions received.
func (r *Requester) Counters() *accounting.Counters { return &r.acct }

// Close releases the underlying connection. It is safe to call before Run (a
// configured-but-never-started flow) and after (Run also closes on return);
// double close is tolerated.
func (r *Requester) Close() error {
	if r.conn != nil {
		return r.conn.Close()
	}
	return nil
}

// Run walks the script until the stop condition or ctx cancellation.
func (r *Requester) Run(ctx context.Context) error {
	defer r.conn.Close()
	if r.after > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.after)
		defer cancel()
	}
	if len(r.script) == 0 {
		return nil
	}
	for {
		for _, step := range r.script {
			if ctx.Err() != nil {
				return nil
			}
			n, err := r.request(int(step.Size.Sample(r.rng)))
			if n > 0 {
				r.acct.Add(uint64(n))
			}
			if err != nil {
				if isTimeout(err) {
					continue // best-effort over UDP: a dropped response, keep going
				}
				return nil // connection closed / fatal: end cleanly
			}
			if r.stopReached(&r.acct) {
				return nil
			}
			if think := time.Duration(step.Think.Sample(r.rng)); think > 0 {
				t := time.NewTimer(think)
				select {
				case <-ctx.Done():
					t.Stop()
					return nil
				case <-t.C:
				}
			}
		}
	}
}

func (r *Requester) stopReached(a *accounting.Counters) bool {
	return (r.count > 0 && a.Packets() >= r.count) || (r.volume > 0 && a.Bytes() >= r.volume)
}

// request sends a request for respSize bytes and reads the full response,
// returning how many bytes arrived.
func (r *Requester) request(respSize int) (int, error) {
	if respSize < 1 {
		respSize = 1
	}
	binary.BigEndian.PutUint64(r.hdr[:], uint64(respSize))
	if _, err := r.conn.Write(r.hdr[:]); err != nil {
		return 0, err
	}
	got := 0
	for got < respSize {
		_ = r.conn.SetReadDeadline(time.Now().Add(reqIOTimeout))
		n, err := r.conn.Read(r.buf)
		got += n
		if err != nil {
			return got, err
		}
	}
	return got, nil
}

// Responder serves request/response traffic: it reads an 8-byte size and returns
// that many bytes to the requester. One responder handles any requested size, so
// it's generic across emulations. It implements flow.Runner; its Counters track
// the response (download) bytes it sends.
type Responder struct {
	transport string
	ln        net.Listener   // tcp
	pc        net.PacketConn // udp
	port      int
	mtu       int
	pattern   []byte
	acct      accounting.Counters
}

// ListenResponder binds an ephemeral port for transport ("tcp"|"udp") on the
// kernel stack.
//
// Deprecated: use NewResponder with an injected netpath.Network. ListenResponder
// is a back-compat wrapper pinned to netpath.Host, so its sessions cannot ride
// a datapath-backed or in-memory network.
func ListenResponder(transport string, mtu int) (*Responder, error) {
	return NewResponder(netpath.Host(netip.Addr{}), transport, mtu)
}

// NewResponder binds an ephemeral port for transport ("tcp"|"udp") on n. The
// caller retains ownership of n; the Responder owns just the listener/socket it
// bound (released by Close/Run).
func NewResponder(n netpath.Network, transport string, mtu int) (*Responder, error) {
	if mtu < 1 {
		mtu = 32 * 1024
	}
	r := &Responder{transport: transport, mtu: mtu, pattern: makePattern(mtu)}
	switch transport {
	case "tcp":
		// ":0" binds all interfaces on an ephemeral port (read back via Port()),
		// matching the UDP receiver datapath so a responder is reachable cross-host.
		ln, err := n.Listen("tcp", ":0")
		if err != nil {
			return nil, err
		}
		r.ln, r.port = ln, addrPort(ln.Addr())
	case "udp":
		pc, err := n.ListenPacket("udp", ":0")
		if err != nil {
			return nil, err
		}
		r.pc, r.port = pc, addrPort(pc.LocalAddr())
	default:
		return nil, fmt.Errorf("responder transport must be tcp or udp, got %q", transport)
	}
	return r, nil
}

// addrPort extracts the port from a bound address without assuming the
// concrete net.Addr type, so any netpath.Network's addresses work (kernel
// *net.TCPAddr/*net.UDPAddr, the memory fabric's "mem:<port>", …).
func addrPort(a net.Addr) int {
	switch t := a.(type) {
	case *net.TCPAddr:
		return t.Port
	case *net.UDPAddr:
		return t.Port
	}
	if _, ps, err := net.SplitHostPort(a.String()); err == nil {
		if p, err := strconv.Atoi(ps); err == nil {
			return p
		}
	}
	return 0
}

// Port returns the bound listen port (for ephemeral-port negotiation).
func (r *Responder) Port() int { return r.port }

// Counters exposes the response bytes served.
func (r *Responder) Counters() *accounting.Counters { return &r.acct }

// Close releases the listener/socket.
func (r *Responder) Close() error {
	if r.ln != nil {
		return r.ln.Close()
	}
	if r.pc != nil {
		return r.pc.Close()
	}
	return nil
}

// Run serves requests until ctx is cancelled.
func (r *Responder) Run(ctx context.Context) error {
	go func() { <-ctx.Done(); _ = r.Close() }() // unblock Accept/ReadFrom
	if r.transport == "tcp" {
		return r.serveTCP(ctx)
	}
	return r.serveUDP(ctx)
}

func (r *Responder) serveTCP(ctx context.Context) error {
	for {
		conn, err := r.ln.Accept()
		if err != nil {
			return nil // listener closed (ctx cancelled)
		}
		go r.handleTCP(ctx, conn)
	}
}

func (r *Responder) handleTCP(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	var hdr [reqHeaderLen]byte
	for {
		if ctx.Err() != nil {
			return
		}
		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			return
		}
		remaining := int(binary.BigEndian.Uint64(hdr[:]))
		for remaining > 0 {
			n := remaining
			if n > len(r.pattern) {
				n = len(r.pattern)
			}
			if _, err := conn.Write(r.pattern[:n]); err != nil {
				return
			}
			r.acct.Add(uint64(n))
			remaining -= n
		}
	}
}

func (r *Responder) serveUDP(ctx context.Context) error {
	hdr := make([]byte, 64)
	for {
		n, src, err := r.pc.ReadFrom(hdr)
		if err != nil {
			return nil // socket closed (ctx cancelled)
		}
		if n < reqHeaderLen {
			continue
		}
		remaining := int(binary.BigEndian.Uint64(hdr[:reqHeaderLen]))
		for remaining > 0 {
			m := remaining
			if m > r.mtu {
				m = r.mtu
			}
			if _, err := r.pc.WriteTo(r.pattern[:m], src); err != nil {
				break
			}
			r.acct.Add(uint64(m))
			remaining -= m
		}
	}
}

func makePattern(n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = 0xA5
	}
	return p
}

func isTimeout(err error) bool {
	var ne net.Error
	return err != nil && (err == io.EOF || (asNetError(err, &ne) && ne.Timeout()))
}

func asNetError(err error, target *net.Error) bool {
	if ne, ok := err.(net.Error); ok {
		*target = ne
		return true
	}
	return false
}
