// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package latency

import (
	"context"
	"encoding/binary"
	"net"
	"time"
)

// UDPPinger measures RTT by sending an 8-byte sequence-tagged datagram and
// waiting for an echo. It works against any UDP echo responder (and, later, a
// loom reflector). ICMP probing (root-only) can be added as another Pinger.
type UDPPinger struct {
	conn net.Conn
}

// NewUDPPinger connects to a UDP echo target at addr (host:port).
func NewUDPPinger(addr string) (*UDPPinger, error) {
	c, err := net.Dial("udp", addr)
	if err != nil {
		return nil, err
	}
	return &UDPPinger{conn: c}, nil
}

// Ping sends one probe and waits for the echo, honoring ctx's deadline.
func (p *UDPPinger) Ping(ctx context.Context, seq uint64) (time.Duration, error) {
	if dl, ok := ctx.Deadline(); ok {
		_ = p.conn.SetDeadline(dl)
	} else {
		_ = p.conn.SetDeadline(time.Now().Add(time.Second))
	}

	var out [8]byte
	binary.BigEndian.PutUint64(out[:], seq)

	start := time.Now()
	if _, err := p.conn.Write(out[:]); err != nil {
		return 0, err
	}
	// Read until the echo carries our sequence number, discarding stale or
	// reordered datagrams from earlier probes. The deadline set above bounds the
	// loop, so a missing echo returns a timeout rather than spinning.
	buf := make([]byte, 64)
	for {
		n, err := p.conn.Read(buf)
		if err != nil {
			return 0, err
		}
		if n >= 8 && binary.BigEndian.Uint64(buf[:8]) == seq {
			return time.Since(start), nil
		}
	}
}

// Close releases the socket.
func (p *UDPPinger) Close() error { return p.conn.Close() }
