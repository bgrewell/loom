// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package vidstream

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/app"
	"github.com/bgrewell/loom/core/app/httpx"
	"github.com/bgrewell/loom/core/metrics"
	"github.com/bgrewell/loom/core/netpath"
)

// memPair returns a connected Memory fabric pair, closed at test end.
func memPair(t *testing.T) (cliNet, srvNet netpath.Network) {
	t.Helper()
	na, nb := netpath.Memory()
	t.Cleanup(func() { _ = na.Close(); _ = nb.Close() })
	return na, nb
}

// buildOrigin builds an httpx HTTPOrigin (the "video" app's far end) through
// the app registry, exactly as the agent would.
func buildOrigin(t *testing.T, n netpath.Network, params map[string]string) app.Server {
	t.Helper()
	srv, err := app.ServerRegistry.Build(httpx.Name, app.Options{Params: params, Network: n, Seed: 11})
	if err != nil {
		t.Fatalf("Build origin: %v", err)
	}
	t.Cleanup(func() { _ = srv.(io.Closer).Close() })
	return srv
}

// buildPlayer builds a "video" client through the app registry, aimed at srv
// over n. The URL host is "localhost": the memory fabric routes by port, and
// "localhost" is among the generated cert's SANs, so TLS tests exercise real
// certificate verification.
func buildPlayer(t *testing.T, n netpath.Network, srv app.Server, params map[string]string) app.Client {
	t.Helper()
	target := fmt.Sprintf("localhost:%d", srv.Addr().Port())
	cli, err := app.ClientRegistry.Build(Name, app.Options{Params: params, Network: n, Target: target, Seed: 13})
	if err != nil {
		t.Fatalf("Build player: %v", err)
	}
	t.Cleanup(func() { _ = cli.(io.Closer).Close() })
	return cli
}

// runPair serves the origin, runs the player to completion, and returns the
// player's cumulative snapshot.
func runPair(t *testing.T, srv app.Server, cli app.Client) metrics.Video {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Run(ctx) }()
	if err := cli.Run(ctx); err != nil {
		t.Fatalf("player Run: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("player run timed out (returned on cancellation, not completion)")
	}
	cum, ok := cli.(interface{ CumulativeMetrics() metrics.Snapshot }).CumulativeMetrics().(metrics.Video)
	if !ok {
		t.Fatal("player CumulativeMetrics is not metrics.Video")
	}
	cancel()
	if err := <-srvDone; err != nil {
		t.Fatalf("origin Run: %v", err)
	}
	return cum
}

// limiter caps the cumulative read rate of every connection sharing it: each
// read blocks until the running total fits under bytes/sec since the current
// rate epoch. setRate opens a new epoch, so a mid-run bandwidth change (the
// handover stand-in) takes effect immediately, even mid-transfer.
type limiter struct {
	mu       sync.Mutex
	rate     float64 // bytes/sec; 0 = unlimited
	epoch    time.Time
	consumed float64
}

func (l *limiter) setRate(bytesPerSec float64) {
	l.mu.Lock()
	l.rate, l.epoch, l.consumed = bytesPerSec, time.Now(), 0
	l.mu.Unlock()
}

func (l *limiter) wait(n int) {
	l.mu.Lock()
	if l.rate <= 0 {
		l.mu.Unlock()
		return
	}
	l.consumed += float64(n)
	due := l.epoch.Add(time.Duration(l.consumed / l.rate * float64(time.Second)))
	l.mu.Unlock()
	if d := time.Until(due); d > 0 {
		time.Sleep(d)
	}
}

// throttledNet wraps a Network so dialed connections read at most the
// limiter's rate — the deterministic bandwidth trace the ABR tests steer.
type throttledNet struct {
	netpath.Network
	lim *limiter
}

func (tn *throttledNet) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	c, err := tn.Network.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return &throttledConn{Conn: c, lim: tn.lim}, nil
}

// throttledConn paces reads in small chunks so the cap applies smoothly
// within a segment, not once per giant read.
type throttledConn struct {
	net.Conn
	lim *limiter
}

func (c *throttledConn) Read(b []byte) (int, error) {
	if len(b) > 8192 {
		b = b[:8192]
	}
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.lim.wait(n)
	}
	return n, err
}

// segsFetched polls the player's cumulative segment count without closing an
// observation window.
func segsFetched(cli app.Client) uint64 {
	v := cli.(interface{ CumulativeMetrics() metrics.Snapshot }).CumulativeMetrics().(metrics.Video)
	return v.SegmentsFetched
}

// waitFor polls cond until it holds or the deadline expires.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestSteadyPlayNoStalls: a full client↔HTTPOrigin run over the memory
// fabric at ample bandwidth — startup populates, the throughput policy
// upshifts off the conservative first rung, and playback never stalls.
func TestSteadyPlayNoStalls(t *testing.T) {
	cn, sn := memPair(t)
	srv := buildOrigin(t, sn, map[string]string{
		"ladder": "l:100k,m:400k,h:1000k", "seg_duration": "500ms", "segments": "4",
	})
	cli := buildPlayer(t, cn, srv, map[string]string{
		"url_name": "movie", "start_threshold": "500ms",
		"buffer_target": "2s", "rebuffer_target": "1s",
	})
	v := runPair(t, srv, cli)

	if v.StartupMs <= 0 {
		t.Errorf("StartupMs = %v, want > 0", v.StartupMs)
	}
	if v.Stalls != 0 || v.StallTimeMs != 0 || len(v.StallEvents) != 0 {
		t.Errorf("clean play recorded stalls: %+v", v)
	}
	if v.SegmentsFetched != 4 {
		t.Errorf("SegmentsFetched = %d, want 4", v.SegmentsFetched)
	}
	if v.RepSwitchesUp < 1 {
		t.Errorf("RepSwitchesUp = %d, want ≥ 1 (ample bandwidth must upshift)", v.RepSwitchesUp)
	}
	if v.RepSwitchesDown != 0 {
		t.Errorf("RepSwitchesDown = %d, want 0", v.RepSwitchesDown)
	}
	if v.AvgBitrateKbps <= 100 {
		t.Errorf("AvgBitrateKbps = %v, want > 100 (not stuck on the bottom rung)", v.AvgBitrateKbps)
	}
	if v.RebufferRatio != 0 {
		t.Errorf("RebufferRatio = %v, want 0", v.RebufferRatio)
	}
	if v.BufferMs != 0 {
		t.Errorf("BufferMs = %v after playing out, want 0", v.BufferMs)
	}
	if kind := cli.(metrics.Source).Metrics().Kind(); kind != metrics.KindVideo {
		t.Errorf("snapshot Kind = %q, want %q", kind, metrics.KindVideo)
	}
	// Requests: master + 2 media playlists (start rung + upshifted rung) +
	// 4 segments, one "packet" each; bytes dominated by the segment bodies.
	if got := cli.Counters().Packets(); got != 7 {
		t.Errorf("counters packets = %d, want 7 (1 master + 2 playlists + 4 segments)", got)
	}
	if got := cli.Counters().Bytes(); got < 190000 {
		t.Errorf("counters bytes = %d, want > 190000 (segment bodies)", got)
	}
}

// TestThrottledStallDownshiftRecovery: the flagship deterministic trace —
// ample bandwidth upshifts to the top rung, then the throttle collapses to
// 200 kbps (a 1000 kbps rung cannot sustain): the buffer drains to zero, a
// timestamped stall event is recorded, the throughput policy downshifts, and
// playback recovers and completes on the low rung.
func TestThrottledStallDownshiftRecovery(t *testing.T) {
	cn, sn := memPair(t)
	lim := &limiter{}
	tn := &throttledNet{Network: cn, lim: lim}
	srv := buildOrigin(t, sn, map[string]string{
		"ladder": "l:100k,h:1000k", "seg_duration": "500ms", "segments": "12",
	})
	cli := buildPlayer(t, tn, srv, map[string]string{
		"url_name": "movie", "start_threshold": "500ms",
		"buffer_target": "1s", "rebuffer_target": "1s", "abr": "throughput",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Run(ctx) }()
	cliDone := make(chan error, 1)
	go func() { cliDone <- cli.Run(ctx) }()

	// Let the player upshift and reach steady state, then collapse the
	// bandwidth below the top rung's bitrate (25 kB/s = 200 kbps).
	waitFor(t, 20*time.Second, "3 segments fetched", func() bool { return segsFetched(cli) >= 3 })
	lim.setRate(25_000)

	if err := <-cliDone; err != nil {
		t.Fatalf("player Run: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("player run timed out")
	}
	v := cli.(interface{ CumulativeMetrics() metrics.Snapshot }).CumulativeMetrics().(metrics.Video)
	cancel()
	if err := <-srvDone; err != nil {
		t.Fatalf("origin Run: %v", err)
	}

	if v.SegmentsFetched != 12 {
		t.Errorf("SegmentsFetched = %d, want 12 (playback must complete)", v.SegmentsFetched)
	}
	if v.Stalls < 1 {
		t.Fatalf("Stalls = %d, want ≥ 1 (buffer must drain under the throttle)", v.Stalls)
	}
	if v.StallTimeMs <= 200 {
		t.Errorf("StallTimeMs = %v, want > 200 (the 1000k segment takes 2.5s at 200kbps)", v.StallTimeMs)
	}
	if len(v.StallEvents) != int(v.Stalls) {
		t.Fatalf("StallEvents = %d, Stalls = %d: every stall must end as a timestamped event", len(v.StallEvents), v.Stalls)
	}
	var evTotal time.Duration
	for _, ev := range v.StallEvents {
		if !ev.End.After(ev.Start) {
			t.Errorf("stall event %v..%v not ordered", ev.Start, ev.End)
		}
		evTotal += ev.End.Sub(ev.Start)
	}
	if diff := math.Abs(float64(evTotal)/float64(time.Millisecond) - v.StallTimeMs); diff > 1 {
		t.Errorf("event durations sum to %v, StallTimeMs %v: correlation timeline out of step", evTotal, v.StallTimeMs)
	}
	if v.RepSwitchesUp < 1 {
		t.Errorf("RepSwitchesUp = %d, want ≥ 1 (ample phase)", v.RepSwitchesUp)
	}
	if v.RepSwitchesDown < 1 {
		t.Errorf("RepSwitchesDown = %d, want ≥ 1 (throughput policy must downshift after the collapse)", v.RepSwitchesDown)
	}
	if v.RebufferRatio <= 0 {
		t.Errorf("RebufferRatio = %v, want > 0", v.RebufferRatio)
	}
	if v.StartupMs <= 0 {
		t.Errorf("StartupMs = %v, want > 0", v.StartupMs)
	}
	if v.BufferMs != 0 {
		t.Errorf("BufferMs = %v after playing out, want 0", v.BufferMs)
	}
}

// TestBufferPolicySwitches: the buffer policy climbs the ladder as the
// buffer fills toward the target and steps down when a throttle drains it —
// switching on buffer level alone.
func TestBufferPolicySwitches(t *testing.T) {
	cn, sn := memPair(t)
	lim := &limiter{}
	tn := &throttledNet{Network: cn, lim: lim}
	srv := buildOrigin(t, sn, map[string]string{
		"ladder": "l:100k,m:300k,h:900k", "seg_duration": "500ms", "segments": "10",
	})
	cli := buildPlayer(t, tn, srv, map[string]string{
		"url_name": "movie", "start_threshold": "500ms",
		"buffer_target": "3s", "rebuffer_target": "1s", "abr": "buffer",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Run(ctx) }()
	cliDone := make(chan error, 1)
	go func() { cliDone <- cli.Run(ctx) }()

	waitFor(t, 20*time.Second, "5 segments fetched", func() bool { return segsFetched(cli) >= 5 })
	lim.setRate(25_000) // 200 kbps: the 900k rung drains the buffer

	if err := <-cliDone; err != nil {
		t.Fatalf("player Run: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("player run timed out")
	}
	v := cli.(interface{ CumulativeMetrics() metrics.Snapshot }).CumulativeMetrics().(metrics.Video)
	cancel()
	if err := <-srvDone; err != nil {
		t.Fatalf("origin Run: %v", err)
	}

	if v.SegmentsFetched != 10 {
		t.Errorf("SegmentsFetched = %d, want 10", v.SegmentsFetched)
	}
	if v.RepSwitchesUp < 1 {
		t.Errorf("RepSwitchesUp = %d, want ≥ 1 (rising buffer must climb the ladder)", v.RepSwitchesUp)
	}
	if v.RepSwitchesDown < 1 {
		t.Errorf("RepSwitchesDown = %d, want ≥ 1 (drained buffer must step down)", v.RepSwitchesDown)
	}
	if v.StartupMs <= 0 {
		t.Errorf("StartupMs = %v, want > 0", v.StartupMs)
	}
}

// TestTLSPassthrough: the shared transport grammar end to end — the player
// pins the origin's self-signed cert via tls_ca and streams over h2 with
// certificate verification ON.
func TestTLSPassthrough(t *testing.T) {
	cn, sn := memPair(t)
	srv := buildOrigin(t, sn, map[string]string{
		"tls": "true", "h2": "true",
		"ladder": "l:100k,h:400k", "seg_duration": "500ms", "segments": "2",
	})
	pem := srv.(interface{ CertificatePEM() []byte }).CertificatePEM()
	if len(pem) == 0 {
		t.Fatal("origin published no certificate PEM")
	}
	cli := buildPlayer(t, cn, srv, map[string]string{
		"url_name": "movie", "start_threshold": "500ms",
		"buffer_target": "1s", "rebuffer_target": "500ms",
		"tls": "true", "h2": "true", "tls_ca": base64.StdEncoding.EncodeToString(pem),
	})
	v := runPair(t, srv, cli)
	if v.SegmentsFetched != 2 || v.Stalls != 0 {
		t.Errorf("TLS run: segments/stalls = %d/%d, want 2/0", v.SegmentsFetched, v.Stalls)
	}
	if v.StartupMs <= 0 {
		t.Errorf("StartupMs = %v, want > 0", v.StartupMs)
	}
}

// TestLadderExpectationRun: a matching expectation streams; a mismatched one
// fails Run with ErrLadderMismatch — the manifest is the truth.
func TestLadderExpectationRun(t *testing.T) {
	cn, sn := memPair(t)
	srv := buildOrigin(t, sn, map[string]string{
		"ladder": "l:100k,h:400k", "seg_duration": "500ms", "segments": "2",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Run(ctx) }()

	ok := buildPlayer(t, cn, srv, map[string]string{
		"url_name": "movie", "ladder": "a:100k,b:400k",
		"start_threshold": "500ms", "buffer_target": "1s", "rebuffer_target": "500ms",
	})
	if err := ok.Run(ctx); err != nil {
		t.Errorf("matching expectation: Run = %v, want nil", err)
	}

	bad := buildPlayer(t, cn, srv, map[string]string{
		"url_name": "movie", "ladder": "a:100k,b:900k",
	})
	if err := bad.Run(ctx); !errors.Is(err, ErrLadderMismatch) {
		t.Errorf("mismatched expectation: Run = %v, want ErrLadderMismatch", err)
	}

	cancel()
	if err := <-srvDone; err != nil {
		t.Fatalf("origin Run: %v", err)
	}
}

// TestWindowMetricsDuringRun: Metrics closes observation intervals while the
// player runs (the telemetry-boundary path), and the intervals sum to the
// cumulative view.
func TestWindowMetricsDuringRun(t *testing.T) {
	cn, sn := memPair(t)
	srv := buildOrigin(t, sn, map[string]string{
		"ladder": "l:100k,h:400k", "seg_duration": "500ms", "segments": "4",
	})
	cli := buildPlayer(t, cn, srv, map[string]string{
		"url_name": "movie", "start_threshold": "500ms",
		"buffer_target": "2s", "rebuffer_target": "1s",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Run(ctx) }()
	cliDone := make(chan error, 1)
	go func() { cliDone <- cli.Run(ctx) }()

	var windows uint64
	waitFor(t, 20*time.Second, "windows to cover all segments", func() bool {
		w := cli.(metrics.Source).Metrics().(metrics.Video)
		windows += w.SegmentsFetched
		return windows >= 4
	})
	if err := <-cliDone; err != nil {
		t.Fatalf("player Run: %v", err)
	}
	cum := cli.(interface{ CumulativeMetrics() metrics.Snapshot }).CumulativeMetrics().(metrics.Video)
	if tail := cum.SegmentsFetched; windows != tail {
		t.Errorf("windowed segments sum to %d, cumulative %d (intervals must partition the run)", windows, tail)
	}
	cancel()
	if err := <-srvDone; err != nil {
		t.Fatalf("origin Run: %v", err)
	}
}
