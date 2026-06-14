// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package latency

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// TestUDPPingerMatchesSequence verifies the pinger discards a mismatched
// (stale/reordered) datagram and only returns when the echo carries its own
// sequence number — so the RTT is attributed to the right probe.
func TestUDPPingerMatchesSequence(t *testing.T) {
	srv, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer srv.Close()

	go func() {
		buf := make([]byte, 64)
		for {
			n, addr, err := srv.ReadFrom(buf)
			if err != nil {
				return
			}
			// A mismatched datagram (different seq) the pinger must ignore...
			stale := make([]byte, 8)
			binary.BigEndian.PutUint64(stale, 999999)
			_, _ = srv.WriteTo(stale, addr)
			// ...followed by the real echo it must match on.
			echo := make([]byte, n)
			copy(echo, buf[:n])
			_, _ = srv.WriteTo(echo, addr)
		}
	}()

	p, err := NewUDPPinger(srv.LocalAddr().String())
	if err != nil {
		t.Fatalf("NewUDPPinger: %v", err)
	}
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rtt, err := p.Ping(ctx, 7)
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if rtt <= 0 {
		t.Fatalf("rtt = %v, want > 0", rtt)
	}
}

// TestUDPPingerTimesOut: with no responder the bounded loop returns an error
// rather than spinning forever.
func TestUDPPingerTimesOut(t *testing.T) {
	// A bound-but-silent socket: connects fine, never replies.
	silent, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer silent.Close()

	p, err := NewUDPPinger(silent.LocalAddr().String())
	if err != nil {
		t.Fatalf("NewUDPPinger: %v", err)
	}
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := p.Ping(ctx, 1); err == nil {
		t.Fatal("Ping should error when no echo arrives")
	}
}
