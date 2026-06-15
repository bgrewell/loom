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
	"time"

	"github.com/bgrewell/loom/core/accounting"
)

// Request/response emulations (e.g. https-browse, ftp-transfer) need real
// bidirectional traffic: a client requests an object and a server returns it, so
// the *download* bytes flow server→client. That is connection-oriented and two-
// way — unlike the one-directional frame datapath — so the requester and
// responder here speak directly over net (TCP or UDP).
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
// and prepares a request/response runner.
func DialRequester(transport, target string, script BehaviorScript, mtu int, after time.Duration, count, volume uint64, seed int64) (*Requester, error) {
	switch transport {
	case "tcp", "udp":
	default:
		return nil, fmt.Errorf("request/response transport must be tcp or udp, got %q", transport)
	}
	c, err := net.Dial(transport, target)
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

// ListenResponder binds an ephemeral port for transport ("tcp"|"udp").
func ListenResponder(transport string, mtu int) (*Responder, error) {
	if mtu < 1 {
		mtu = 32 * 1024
	}
	r := &Responder{transport: transport, mtu: mtu, pattern: makePattern(mtu)}
	switch transport {
	case "tcp":
		// ":0" binds all interfaces on an ephemeral port (read back via Port()),
		// matching the UDP receiver datapath so a responder is reachable cross-host.
		ln, err := net.Listen("tcp", ":0")
		if err != nil {
			return nil, err
		}
		r.ln, r.port = ln, ln.Addr().(*net.TCPAddr).Port
	case "udp":
		pc, err := net.ListenPacket("udp", ":0")
		if err != nil {
			return nil, err
		}
		r.pc, r.port = pc, pc.LocalAddr().(*net.UDPAddr).Port
	default:
		return nil, fmt.Errorf("responder transport must be tcp or udp, got %q", transport)
	}
	return r, nil
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
