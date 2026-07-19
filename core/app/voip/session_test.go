// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// In-package tests: the Phase 2 exit criteria drive real bidirectional calls
// over the in-memory network and reach into unexported state (RTCP interval
// shortening, latch internals) that the wire defaults would make too slow to
// observe in CI.
package voip

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/netpath"
	"github.com/bgrewell/loom/core/rtp"
	"github.com/bgrewell/loom/core/rtp/codec"
)

// testRTCPTmin shortens the RTCP interval so XR/RTT/OWD plumbing is
// exercised within CI-sized calls (the wire default is 5 s).
const testRTCPTmin = 100 * time.Millisecond

// zeroOffset is an OffsetProvider asserting synchronized clocks (true in a
// single-process test) with a small error bound.
type zeroOffset struct{}

func (zeroOffset) Offset() (time.Duration, time.Duration, bool) {
	return 0, time.Millisecond, true
}

func mustCodec(t *testing.T, name string) codec.Codec {
	t.Helper()
	c, err := codec.ByName(name)
	if err != nil {
		t.Fatalf("codec.ByName(%q): %v", name, err)
	}
	return c
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", d, what)
}

// startSession runs s and returns a stop func that cancels and reports Run's
// error.
func startSession(s *MediaSession) (stop func() error) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	return func() error {
		cancel()
		return <-done
	}
}

// TestBidirectionalCall is exit criterion (1): a full two-way G.711 call over
// netpath.Memory with sane zero-impairment scores on both ends, a populated
// remote XR view, and a clean BYE teardown — while Metrics is hammered
// concurrently with Run (exercised under -race).
func TestBidirectionalCall(t *testing.T) {
	na, nb := netpath.Memory()
	defer na.Close()
	defer nb.Close()

	// A roomy jitter buffer keeps CI scheduler hiccups from counting as
	// discards; Ta ≈ 120+20+0.25 ms costs only ~0.05 Idd points, so the
	// zero-impairment MOS band still holds.
	ans, err := NewMediaSession(nb, MediaConfig{Codec: mustCodec(t, "pcmu"), JitterBufferMs: 120}, zeroOffset{})
	if err != nil {
		t.Fatalf("NewMediaSession(answerer): %v", err)
	}
	ans.setRTCPTmin(testRTCPTmin)
	remote := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), ans.LocalAddr().Port())
	cal, err := NewMediaSession(na, MediaConfig{Codec: mustCodec(t, "pcmu"), JitterBufferMs: 120, RemoteRTP: remote}, zeroOffset{})
	if err != nil {
		t.Fatalf("NewMediaSession(caller): %v", err)
	}
	cal.setRTCPTmin(testRTCPTmin)

	stopAns := startSession(ans)
	stopCal := startSession(cal)

	// Hammer Metrics concurrently with the running session (race exercise).
	hammerCtx, hammerCancel := context.WithCancel(context.Background())
	var hammer sync.WaitGroup
	hammer.Add(1)
	go func() {
		defer hammer.Done()
		for hammerCtx.Err() == nil {
			_ = cal.Metrics()
			_ = ans.Metrics()
			time.Sleep(5 * time.Millisecond)
		}
	}()

	waitFor(t, 10*time.Second, "media and XR exchange on both ends", func() bool {
		cm, am := cal.Metrics(), ans.Metrics()
		return cm.RxPackets > 30 && am.RxPackets > 30 &&
			cm.RemoteMOSCQ > 0 && am.RemoteMOSCQ > 0 &&
			cm.RTTMs > 0 && am.RTTMs > 0
	})
	hammerCancel()
	hammer.Wait()
	// Settle one interval so the final snapshots cover fresh traffic.
	time.Sleep(200 * time.Millisecond)

	check := func(name string, s *MediaSession) {
		v := s.Metrics()
		if v.Codec != "pcmu" {
			t.Errorf("%s: Codec = %q, want pcmu", name, v.Codec)
		}
		if v.MOSCQ < 4.2 || v.MOSCQ > 4.5 {
			t.Errorf("%s: MOSCQ = %.3f, want ~4.4 (zero impairment)", name, v.MOSCQ)
		}
		if v.RFactor < 85 || v.RFactor > 94 {
			t.Errorf("%s: RFactor = %.2f, want ≈93 (zero impairment)", name, v.RFactor)
		}
		if v.LossPct > 1 {
			t.Errorf("%s: LossPct = %.2f, want ~0", name, v.LossPct)
		}
		if v.OWDMethod != "timesync" {
			t.Errorf("%s: OWDMethod = %q, want timesync", name, v.OWDMethod)
		}
		if v.OWDMs < -5 || v.OWDMs > 100 {
			t.Errorf("%s: OWDMs = %.2f, want ≈0 in-process", name, v.OWDMs)
		}
		if v.RemoteMOSCQ < 3 || v.RemoteMOSCQ > 5 {
			t.Errorf("%s: RemoteMOSCQ = %.2f, want a populated sane remote view", name, v.RemoteMOSCQ)
		}
		if v.EModel.R == 0 || v.EModel.Ro == 0 {
			t.Errorf("%s: EModel breakdown not populated: %+v", name, v.EModel)
		}
		if v.TxPackets == 0 || v.RxPackets == 0 {
			t.Errorf("%s: TxPackets=%d RxPackets=%d, want both nonzero", name, v.TxPackets, v.RxPackets)
		}
		if s.Counters().Bytes() == 0 {
			t.Errorf("%s: accounting counters empty", name)
		}
	}
	check("caller", cal)
	check("answerer", ans)

	// Clean teardown: the caller's BYE compound must reach the still-running
	// answerer before the answerer itself is stopped.
	if err := stopCal(); err != nil {
		t.Fatalf("caller Run: %v", err)
	}
	waitFor(t, 5*time.Second, "answerer to see the caller's BYE", func() bool {
		return ans.Metrics().RemoteBye
	})
	if err := stopAns(); err != nil {
		t.Fatalf("answerer Run: %v", err)
	}
}

// TestAnswererLatch is exit criterion (2): the answerer locks onto the first
// source passing validity + probation and counts everyone else as stray —
// both a pre-latch loser and post-latch interlopers.
func TestAnswererLatch(t *testing.T) {
	na, nb := netpath.Memory()
	defer na.Close()
	defer nb.Close()

	ans, err := NewMediaSession(nb, MediaConfig{Codec: mustCodec(t, "pcmu")}, nil)
	if err != nil {
		t.Fatalf("NewMediaSession(answerer): %v", err)
	}
	ans.setRTCPTmin(testRTCPTmin)
	stopAns := startSession(ans)
	defer stopAns()

	// A stray source: valid RTP (right PT) from its own socket and SSRC. One
	// packet only — it never passes probation, so it can only lose the race.
	strayPC, err := na.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatalf("stray ListenPacket: %v", err)
	}
	defer strayPC.Close()
	const straySSRC = 0xDEADBEEF
	sendStray := func(seq uint16) {
		h := rtp.Header{PayloadType: 0, SequenceNumber: seq, Timestamp: uint32(seq) * 160, SSRC: straySSRC}
		buf := make([]byte, rtp.HeaderLen+160)
		n, err := h.MarshalTo(buf)
		if err != nil {
			t.Fatalf("MarshalTo: %v", err)
		}
		dst := &net.UDPAddr{Port: int(ans.LocalAddr().Port())}
		if _, err := strayPC.WriteTo(buf[:n+160], dst); err != nil {
			t.Fatalf("stray WriteTo: %v", err)
		}
	}
	sendStray(1) // pre-latch candidate that will lose

	cal, err := NewMediaSession(na, MediaConfig{
		Codec:     mustCodec(t, "pcmu"),
		RemoteRTP: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), ans.LocalAddr().Port()),
	}, nil)
	if err != nil {
		t.Fatalf("NewMediaSession(caller): %v", err)
	}
	cal.setRTCPTmin(testRTCPTmin)
	stopCal := startSession(cal)
	defer stopCal()

	waitFor(t, 5*time.Second, "latch onto the caller", func() bool {
		ans.mu.Lock()
		defer ans.mu.Unlock()
		return ans.latched
	})
	ans.mu.Lock()
	gotSSRC := ans.remoteSSRC
	ans.mu.Unlock()
	if gotSSRC == straySSRC {
		t.Fatalf("answerer latched the stray SSRC %08x", straySSRC)
	}
	// The pre-latch loser's packet is counted when the winner latches.
	waitFor(t, 2*time.Second, "pre-latch stray to be counted", func() bool {
		return ans.StrayPackets() >= 1
	})

	before := ans.StrayPackets()
	for i := 0; i < 3; i++ {
		sendStray(uint16(10 + i))
	}
	waitFor(t, 2*time.Second, "post-latch strays to be counted", func() bool {
		return ans.StrayPackets() >= before+3
	})
	if v := ans.Metrics(); v.RxPackets == 0 {
		t.Error("answerer counted no packets from the latched source")
	}
}

// TestRecvOnlyCaller: a recvonly caller sends no RTP, so the answerer can
// only learn its return address from RTCP (symmetric-RTCP adoption). The
// call must still establish: the answerer sends media toward the RTCP
// source, the caller latches and scores it, and no handshake error fires.
func TestRecvOnlyCaller(t *testing.T) {
	na, nb := netpath.Memory()
	defer na.Close()
	defer nb.Close()

	ans, err := NewMediaSession(nb, MediaConfig{Codec: mustCodec(t, "pcmu"), JitterBufferMs: 120}, zeroOffset{})
	if err != nil {
		t.Fatalf("NewMediaSession(answerer): %v", err)
	}
	ans.setRTCPTmin(testRTCPTmin)
	cal, err := NewMediaSession(na, MediaConfig{
		Codec:          mustCodec(t, "pcmu"),
		JitterBufferMs: 120,
		Direction:      RecvOnly,
		RemoteRTP:      netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), ans.LocalAddr().Port()),
	}, zeroOffset{})
	if err != nil {
		t.Fatalf("NewMediaSession(caller): %v", err)
	}
	cal.setRTCPTmin(testRTCPTmin)

	stopAns := startSession(ans)
	stopCal := startSession(cal)
	defer stopAns()

	waitFor(t, 10*time.Second, "recvonly caller to receive scored media", func() bool {
		cv, av := cal.Metrics(), ans.Metrics()
		return cv.RxPackets > 30 && cv.MOSCQ > 0 && av.TxPackets > 30
	})
	if v := cal.Metrics(); v.TxPackets != 0 {
		t.Errorf("recvonly caller sent %d RTP packets, want 0", v.TxPackets)
	}
	if err := stopCal(); err != nil {
		t.Fatalf("recvonly caller Run: %v (handshake must be satisfied by return media)", err)
	}
}

// TestHandshakeTimeout is exit criterion (3): a caller with no live peer
// fails with the typed *HandshakeError once the timeout elapses.
func TestHandshakeTimeout(t *testing.T) {
	na, nb := netpath.Memory()
	defer na.Close()
	defer nb.Close()

	cal, err := NewMediaSession(na, MediaConfig{
		Codec:            mustCodec(t, "pcmu"),
		RemoteRTP:        netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), 40404), // unbound
		HandshakeTimeout: 200 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("NewMediaSession: %v", err)
	}
	start := time.Now()
	err = cal.Run(context.Background())
	elapsed := time.Since(start)
	var hs *HandshakeError
	if !errors.As(err, &hs) {
		t.Fatalf("Run = %v, want *HandshakeError", err)
	}
	if hs.Timeout != 200*time.Millisecond {
		t.Errorf("HandshakeError.Timeout = %v, want 200ms", hs.Timeout)
	}
	if elapsed < 200*time.Millisecond {
		t.Errorf("Run returned after %v, before the handshake timeout", elapsed)
	}
}

// delayNet wraps a Network so every k-th datagram read (after a warmup) is
// delivered late — the packets are on time on the wire but stamped late at
// the receiver, exactly what a delay spike does to a jitter buffer.
type delayNet struct {
	netpath.Network
	after int
	every int
	delay time.Duration
}

func (d *delayNet) ListenPacket(network, address string) (net.PacketConn, error) {
	pc, err := d.Network.ListenPacket(network, address)
	if err != nil {
		return nil, err
	}
	return &delayPC{PacketConn: pc, after: d.after, every: d.every, delay: d.delay}, nil
}

type delayPC struct {
	net.PacketConn
	mu    sync.Mutex
	n     int
	after int
	every int
	delay time.Duration
}

func (p *delayPC) ReadFrom(b []byte) (int, net.Addr, error) {
	n, a, err := p.PacketConn.ReadFrom(b)
	if err != nil {
		return n, a, err
	}
	p.mu.Lock()
	p.n++
	k := p.n
	p.mu.Unlock()
	if k > p.after && k%p.every == 0 {
		time.Sleep(p.delay)
	}
	return n, a, err
}

// TestJitterBufferDiscard is exit criterion (4): injected delivery delay
// turns into DiscardPct > 0 (RFC 3611 discard semantics) and a visibly lower
// MOS even with zero network loss.
func TestJitterBufferDiscard(t *testing.T) {
	na, nb := netpath.Memory()
	defer na.Close()
	defer nb.Close()

	// Every 4th read on the answerer's socket (after latch warmup) stalls
	// 200 ms — far past the 40 ms playout point, and it makes the queued
	// packets behind it late too.
	slow := &delayNet{Network: nb, after: 20, every: 4, delay: 200 * time.Millisecond}
	ans, err := NewMediaSession(slow, MediaConfig{Codec: mustCodec(t, "pcmu")}, zeroOffset{})
	if err != nil {
		t.Fatalf("NewMediaSession(answerer): %v", err)
	}
	ans.setRTCPTmin(testRTCPTmin)
	cal, err := NewMediaSession(na, MediaConfig{
		Codec:     mustCodec(t, "pcmu"),
		RemoteRTP: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), ans.LocalAddr().Port()),
	}, zeroOffset{})
	if err != nil {
		t.Fatalf("NewMediaSession(caller): %v", err)
	}
	cal.setRTCPTmin(testRTCPTmin)

	stopAns := startSession(ans)
	stopCal := startSession(cal)
	defer stopAns()
	defer stopCal()

	waitFor(t, 10*time.Second, "discards to accumulate", func() bool {
		ans.mu.Lock()
		defer ans.mu.Unlock()
		return ans.discards >= 5
	})
	v := ans.Metrics() // first Metrics call: the interval spans the whole run
	if v.DiscardPct <= 0 {
		t.Fatalf("DiscardPct = %v, want > 0 under injected delay", v.DiscardPct)
	}
	// The memory fabric drops nothing: the impairment must be attributed to
	// the discard channel, not absorbed into loss (pins discard→Ppl, not
	// merely "MOS dropped somehow").
	if v.LossPct > 1 {
		t.Errorf("LossPct = %.2f, want ~0 (delay must count as discard, not loss)", v.LossPct)
	}
	if v.MOSCQ >= 4.2 {
		t.Errorf("MOSCQ = %.3f, want a visible drop below the ~4.4 baseline", v.MOSCQ)
	}
	if v.MOSCQ < 1 {
		t.Errorf("MOSCQ = %.3f, below the MOS floor", v.MOSCQ)
	}
}

// TestEphemeralEvenPort: a zero LocalRTP port prefers an even ephemeral bind
// (RFC 3550 §11 convention).
func TestEphemeralEvenPort(t *testing.T) {
	_, nb := netpath.Memory()
	defer nb.Close()
	s, err := NewMediaSession(nb, MediaConfig{Codec: mustCodec(t, "pcmu")}, nil)
	if err != nil {
		t.Fatalf("NewMediaSession: %v", err)
	}
	if p := s.LocalAddr().Port(); p == 0 || p%2 != 0 {
		t.Errorf("LocalAddr port = %d, want a nonzero even port", p)
	}
}

// TestConfigValidation pins the constructor's rejection of unusable configs
// and the documented defaults.
func TestConfigValidation(t *testing.T) {
	_, nb := netpath.Memory()
	defer nb.Close()
	if _, err := NewMediaSession(nil, MediaConfig{Codec: mustCodec(t, "pcmu")}, nil); err == nil {
		t.Error("nil network accepted")
	}
	if _, err := NewMediaSession(nb, MediaConfig{}, nil); err == nil {
		t.Error("empty codec accepted")
	}
	if _, err := NewMediaSession(nb, MediaConfig{Codec: mustCodec(t, "pcmu"), Direction: Direction(9)}, nil); err == nil {
		t.Error("invalid direction accepted")
	}
	if _, err := NewMediaSession(nb, MediaConfig{Codec: mustCodec(t, "pcmu"), JitterBufferMs: -1}, nil); err == nil {
		t.Error("negative jitter buffer accepted")
	}
	// Name-only codec rows resolve through the table.
	s, err := NewMediaSession(nb, MediaConfig{Codec: codec.Codec{Name: "pcmu"}}, nil)
	if err != nil {
		t.Fatalf("name-only codec: %v", err)
	}
	if s.cfg.HandshakeTimeout != DefaultHandshakeTimeout {
		t.Errorf("HandshakeTimeout default = %v, want %v", s.cfg.HandshakeTimeout, DefaultHandshakeTimeout)
	}
	if s.cfg.JitterBufferMs != DefaultJitterBufferMs {
		t.Errorf("JitterBufferMs default = %d, want %d", s.cfg.JitterBufferMs, DefaultJitterBufferMs)
	}
}
