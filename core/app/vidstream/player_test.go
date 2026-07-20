// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package vidstream

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/app"
	"github.com/bgrewell/loom/core/app/httpx"
	"github.com/bgrewell/loom/core/metrics"
	"github.com/bgrewell/loom/core/netpath"
)

// fakeClock is an injectable playhead clock for deterministic model tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Set(t time.Time) {
	f.mu.Lock()
	f.t = t
	f.mu.Unlock()
}

// t0 is the model tests' epoch.
var t0 = time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

// newModelPlayer returns a bare player on a fake clock, positioned at run
// start (the state Run establishes before fetching).
func newModelPlayer(clk *fakeClock) *client {
	c := &client{
		startThreshold: time.Second,
		bufferTarget:   4 * time.Second,
		rebufferTarget: time.Second,
		policy:         abrThroughput,
		now:            clk.Now,
	}
	c.runStart, c.lastSync = clk.Now(), clk.Now()
	return c
}

// TestPlayheadModel drives the virtual playhead through startup, an exact
// mid-stream stall, rebuffering and steady play on a fake clock, pinning the
// snapshot JSON (the core/metrics golden discipline) and the window-versus-
// cumulative semantics.
func TestPlayheadModel(t *testing.T) {
	clk := &fakeClock{t: t0}
	c := newModelPlayer(clk)

	// One 1s segment at t0+250ms: reaches the 1s start threshold → playing.
	clk.Set(t0.Add(250 * time.Millisecond))
	c.recordSegment(100, time.Second, 1000, time.Millisecond)

	// Next segment lands at t0+2.25s: the buffer ran dry at exactly
	// t0+1.25s (250ms fetch instant + 1s of media), so the player stalled
	// there; the segment refills to the rebuffer target and playback
	// resumes at t0+2.25s.
	clk.Set(t0.Add(2250 * time.Millisecond))
	c.recordSegment(1000, time.Second, 1000, time.Millisecond)

	// Snapshot at t0+2.75s: 500ms more played, 500ms of buffer left.
	clk.Set(t0.Add(2750 * time.Millisecond))
	cum, ok := c.CumulativeMetrics().(metrics.Video)
	if !ok {
		t.Fatal("CumulativeMetrics is not metrics.Video")
	}
	got, err := json.Marshal(cum)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// rebuffer_ratio is stall/(stall+play): 1000ms stalled, 1500ms played.
	want := `{"segments_fetched":2,"stalls":1,"startup_ms":250,` +
		`"stall_time_ms":1000,"rebuffer_ratio":0.4,` +
		`"buffer_ms":500,"avg_bitrate_kbps":550,"rep_switches_up":0,` +
		`"rep_switches_down":0,"stall_events":[{"start":"2026-07-17T12:00:01.25Z",` +
		`"end":"2026-07-17T12:00:02.25Z","packets_lost":0}]}`
	if string(got) != want {
		t.Errorf("golden mismatch\n got: %s\nwant: %s", got, want)
	}

	// The cumulative read must not have closed a window: the first Metrics
	// call still sees everything.
	w1, ok := c.Metrics().(metrics.Video)
	if !ok {
		t.Fatal("Metrics is not metrics.Video")
	}
	if w1.SegmentsFetched != 2 || w1.Stalls != 1 || len(w1.StallEvents) != 1 {
		t.Errorf("window 1 = %+v, want the full run (cumulative must not consume it)", w1)
	}

	// A second window sees only what happened since: one clean segment.
	clk.Set(t0.Add(3 * time.Second))
	c.recordSegment(1000, time.Second, 1000, time.Millisecond)
	w2, ok := c.Metrics().(metrics.Video)
	if !ok {
		t.Fatal("Metrics is not metrics.Video")
	}
	if w2.SegmentsFetched != 1 || w2.Stalls != 0 || w2.StallTimeMs != 0 || w2.StallEvents != nil {
		t.Errorf("window 2 = %+v, want 1 clean segment", w2)
	}
	if w2.RebufferRatio != 0 {
		t.Errorf("window 2 RebufferRatio = %v, want 0", w2.RebufferRatio)
	}
	if w2.AvgBitrateKbps != 1000 {
		t.Errorf("window 2 AvgBitrateKbps = %v, want 1000 (window segments only)", w2.AvgBitrateKbps)
	}
	if w2.BufferMs != 1250 {
		t.Errorf("window 2 BufferMs = %v, want 1250", w2.BufferMs)
	}
	if w2.StartupMs != 250 {
		t.Errorf("window 2 StartupMs = %v, want 250 (run-scoped, not windowed)", w2.StartupMs)
	}

	// Cumulative still covers the whole run.
	cum2 := c.CumulativeMetrics().(metrics.Video)
	if cum2.SegmentsFetched != 3 || cum2.Stalls != 1 || len(cum2.StallEvents) != 1 {
		t.Errorf("cumulative = %+v, want 3 segments / 1 stall", cum2)
	}
}

// TestEndOfStreamIsNotAStall: running dry after the last segment is
// completion — no stall is recorded and the state settles at ended.
func TestEndOfStreamIsNotAStall(t *testing.T) {
	clk := &fakeClock{t: t0}
	c := newModelPlayer(clk)

	clk.Set(t0.Add(100 * time.Millisecond))
	c.recordSegment(100, time.Second, 1000, time.Millisecond) // playing
	c.noMoreSegments()

	clk.Set(t0.Add(5 * time.Second)) // way past the buffered second
	v := c.CumulativeMetrics().(metrics.Video)
	if v.Stalls != 0 || v.StallTimeMs != 0 || v.StallEvents != nil {
		t.Errorf("end-of-stream drain recorded a stall: %+v", v)
	}
	if v.BufferMs != 0 {
		t.Errorf("BufferMs = %v after playing out, want 0", v.BufferMs)
	}
	c.mu.Lock()
	st := c.state
	c.mu.Unlock()
	if st != stateEnded {
		t.Errorf("state = %v, want stateEnded", st)
	}
}

// TestEndOfStreamClosesOpenStall: a stall still open when the stream ends is
// closed at that instant (its event emitted) rather than left dangling.
func TestEndOfStreamClosesOpenStall(t *testing.T) {
	clk := &fakeClock{t: t0}
	c := newModelPlayer(clk)

	clk.Set(t0.Add(100 * time.Millisecond))
	c.recordSegment(100, time.Second, 1000, time.Millisecond) // playing until t0+1.1s

	clk.Set(t0.Add(2100 * time.Millisecond))
	c.noMoreSegments() // stalled since t0+1.1s; end closes it

	v := c.CumulativeMetrics().(metrics.Video)
	if v.Stalls != 1 || len(v.StallEvents) != 1 {
		t.Fatalf("snapshot = %+v, want exactly one closed stall", v)
	}
	ev := v.StallEvents[0]
	if !ev.Start.Equal(t0.Add(1100*time.Millisecond)) || !ev.End.Equal(t0.Add(2100*time.Millisecond)) {
		t.Errorf("stall event = %v..%v, want exact onset/close instants", ev.Start, ev.End)
	}
	if v.StallTimeMs != 1000 {
		t.Errorf("StallTimeMs = %v, want 1000", v.StallTimeMs)
	}
}

// TestAllStallWindowRatioIsOne: an observation interval that is 100% stall
// must report RebufferRatio 1.0 — the stall/(stall+play) form is defined even
// with no play time, where a silent 0 would read as a clean interval at the
// worst possible moment.
func TestAllStallWindowRatioIsOne(t *testing.T) {
	clk := &fakeClock{t: t0}
	c := newModelPlayer(clk)

	// 1s segment at t0+100ms → playing; buffer dry at t0+1.1s → stalled.
	clk.Set(t0.Add(100 * time.Millisecond))
	c.recordSegment(100, time.Second, 1000, time.Millisecond)

	// Close a window at t0+2s (mid-stall), then another at t0+3s: the second
	// window is pure stall.
	clk.Set(t0.Add(2 * time.Second))
	c.Metrics()
	clk.Set(t0.Add(3 * time.Second))
	w := c.Metrics().(metrics.Video)
	if w.StallTimeMs != 1000 || w.RebufferRatio != 1 {
		t.Errorf("all-stall window: StallTimeMs = %v, RebufferRatio = %v, want 1000 / 1", w.StallTimeMs, w.RebufferRatio)
	}
}

// TestFreezeParksPlayheadAndClosesOpenStall: freeze (run every Run exit,
// including ctx cancellation mid-stall — the handover/outage case) closes the
// open stall as a timestamped event at the exit instant and stops all further
// accrual, so post-run reads are stable.
func TestFreezeParksPlayheadAndClosesOpenStall(t *testing.T) {
	clk := &fakeClock{t: t0}
	c := newModelPlayer(clk)

	// 1s segment at t0+100ms → playing; buffer dry at t0+1.1s → stalled.
	clk.Set(t0.Add(100 * time.Millisecond))
	c.recordSegment(100, time.Second, 1000, time.Millisecond)

	// The flow is cancelled at t0+2.1s with the stall still open.
	clk.Set(t0.Add(2100 * time.Millisecond))
	c.freeze()

	v := c.CumulativeMetrics().(metrics.Video)
	if v.Stalls != 1 || len(v.StallEvents) != 1 {
		t.Fatalf("snapshot = %+v, want the open stall closed as one event", v)
	}
	ev := v.StallEvents[0]
	if !ev.Start.Equal(t0.Add(1100*time.Millisecond)) || !ev.End.Equal(t0.Add(2100*time.Millisecond)) {
		t.Errorf("stall event = %v..%v, want t0+1.1s..t0+2.1s (closed at the exit instant)", ev.Start, ev.End)
	}
	if v.StallTimeMs != 1000 {
		t.Errorf("StallTimeMs = %v, want 1000", v.StallTimeMs)
	}

	// Post-run reads must not keep accruing stall time.
	clk.Set(t0.Add(time.Hour))
	v2 := c.CumulativeMetrics().(metrics.Video)
	if v2.StallTimeMs != 1000 || v2.Stalls != 1 || len(v2.StallEvents) != 1 {
		t.Errorf("post-freeze snapshot drifted: %+v (playhead must be parked)", v2)
	}
}

// TestPickThroughput pins the throughput policy: highest rung within the
// safety fraction of the estimate, lowest when nothing fits.
func TestPickThroughput(t *testing.T) {
	ladder := []rendition{{kbps: 100}, {kbps: 400}, {kbps: 1000}}
	cases := []struct {
		est  float64
		want int
	}{
		{50, 0},    // nothing fits → lowest
		{124, 0},   // 0.8×124 < 100
		{125, 0},   // 0.8×125 = 100 → rung 0
		{499, 0},   // 0.8×499 < 400
		{500, 1},   // 0.8×500 = 400
		{1249, 1},  // just under the top rung
		{1250, 2},  // 0.8×1250 = 1000
		{50000, 2}, // ample → top
	}
	for _, tc := range cases {
		if got := pickThroughput(ladder, tc.est); got != tc.want {
			t.Errorf("pickThroughput(est=%v) = %d, want %d", tc.est, got, tc.want)
		}
	}
}

// TestPickBuffer pins the buffer policy's threshold map: lowest at/below the
// reservoir, highest at/above the cushion, linear steps between.
func TestPickBuffer(t *testing.T) {
	res, cush := time.Second, 3*time.Second
	cases := []struct {
		n      int
		buffer time.Duration
		want   int
	}{
		{3, 0, 0},
		{3, time.Second, 0},             // at the reservoir
		{3, 1500 * time.Millisecond, 0}, // first third
		{3, 2 * time.Second, 1},         // second third
		{3, 2700 * time.Millisecond, 2}, // last third
		{3, 3 * time.Second, 2},         // at the cushion
		{3, 10 * time.Second, 2},        // above
		{1, 10 * time.Second, 0},        // single rung
		{2, 2 * time.Second, 1},         // midpoint of two rungs
		{3, 500 * time.Millisecond, 0},  // below reservoir
	}
	for _, tc := range cases {
		if got := pickBuffer(tc.n, tc.buffer, res, cush); got != tc.want {
			t.Errorf("pickBuffer(n=%d, buffer=%v) = %d, want %d", tc.n, tc.buffer, got, tc.want)
		}
	}
	// Degenerate cushion==reservoir: any buffer above it maps to the top.
	if got := pickBuffer(3, 2*time.Second, time.Second, time.Second); got != 2 {
		t.Errorf("pickBuffer(cushion==reservoir, above) = %d, want 2", got)
	}
	if got := pickBuffer(3, time.Second, time.Second, time.Second); got != 0 {
		t.Errorf("pickBuffer(cushion==reservoir, at) = %d, want 0", got)
	}
}

// TestHarmonicEWMA: the estimate collapses within a couple of samples after
// a bandwidth drop, no matter how fast the earlier phase was — the property
// the throughput policy's downshift latency rests on.
func TestHarmonicEWMA(t *testing.T) {
	c := &client{}
	c.estObserveLocked(5e6) // multi-Gbps memory-fabric reading
	c.estObserveLocked(200)
	if est := 1 / c.estInv; est > 700 {
		t.Errorf("estimate after one slow sample = %.0f kbps, want ≤ 700 (fast collapse)", est)
	}
	c.estObserveLocked(200)
	if est := 1 / c.estInv; est > 400 {
		t.Errorf("estimate after two slow samples = %.0f kbps, want ≤ 400", est)
	}
	// Non-positive samples are ignored, not folded in as infinities.
	prev := c.estInv
	c.estObserveLocked(0)
	if c.estInv != prev {
		t.Error("zero sample mutated the estimator")
	}
}

// TestParseMaster covers the master-manifest subset the origin emits:
// resolution against the manifest URL, bitrate sort, and the malformed
// cases.
func TestParseMaster(t *testing.T) {
	base, _ := url.Parse("http://origin:80/media/movie/manifest.m3u8")
	body := "#EXTM3U\n#EXT-X-VERSION:3\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=2500000,NAME=\"720p,hd\"\n2500/playlist.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=400000,NAME=\"240p\"\n400/playlist.m3u8\n"
	ladder, err := parseMaster(base, body)
	if err != nil {
		t.Fatalf("parseMaster: %v", err)
	}
	if len(ladder) != 2 {
		t.Fatalf("got %d renditions, want 2", len(ladder))
	}
	if ladder[0].kbps != 400 || ladder[1].kbps != 2500 {
		t.Errorf("ladder not sorted ascending: %d, %d", ladder[0].kbps, ladder[1].kbps)
	}
	if got := ladder[0].playlist.String(); got != "http://origin:80/media/movie/400/playlist.m3u8" {
		t.Errorf("playlist URL = %q (relative URI must resolve against the manifest)", got)
	}
	if ladder[1].label != "720p,hd" {
		t.Errorf("label = %q, want the quoted comma preserved", ladder[1].label)
	}
	for name, bad := range map[string]string{
		"not m3u8":     "<html>",
		"no variants":  "#EXTM3U\n#EXT-X-VERSION:3\n",
		"no bandwidth": "#EXTM3U\n#EXT-X-STREAM-INF:NAME=\"x\"\n400/playlist.m3u8\n",
	} {
		if _, err := parseMaster(base, bad); err == nil {
			t.Errorf("parseMaster(%s) succeeded, want error", name)
		}
	}
}

// TestParseMedia covers the media playlist: EXTINF durations, URI
// resolution, and the malformed cases.
func TestParseMedia(t *testing.T) {
	base, _ := url.Parse("http://origin:80/media/movie/400/playlist.m3u8")
	body := "#EXTM3U\n#EXT-X-TARGETDURATION:1\n" +
		"#EXTINF:0.500,\nseg0\n#EXTINF:0.500,\nseg1\n#EXT-X-ENDLIST\n"
	segs, err := parseMedia(base, body)
	if err != nil {
		t.Fatalf("parseMedia: %v", err)
	}
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want 2", len(segs))
	}
	if segs[1].url.String() != "http://origin:80/media/movie/400/seg1" {
		t.Errorf("segment URL = %q", segs[1].url.String())
	}
	if segs[0].dur != 500*time.Millisecond {
		t.Errorf("segment duration = %v, want 500ms", segs[0].dur)
	}
	for name, bad := range map[string]string{
		"not m3u8":    "hello",
		"no segments": "#EXTM3U\n#EXT-X-ENDLIST\n",
		"bad extinf":  "#EXTM3U\n#EXTINF:soon,\nseg0\n",
	} {
		if _, err := parseMedia(base, bad); err == nil {
			t.Errorf("parseMedia(%s) succeeded, want error", name)
		}
	}
}

// TestFactoryErrors pins Build-time validation: every bad knob is an error
// at Build, not a surprise mid-flow.
func TestFactoryErrors(t *testing.T) {
	_, nb := netpath.Memory()
	defer nb.Close()
	tgt := "localhost:80"
	cases := []struct {
		name string
		opts app.Options
	}{
		{"nil network", app.Options{Target: tgt}},
		{"missing target", app.Options{Network: nb}},
		{"url_name with slash", app.Options{Network: nb, Target: tgt, Params: map[string]string{"url_name": "a/b"}}},
		{"bad abr", app.Options{Network: nb, Target: tgt, Params: map[string]string{"abr": "psychic"}}},
		{"bad ladder", app.Options{Network: nb, Target: tgt, Params: map[string]string{"ladder": "240p400k"}}},
		{"negative seg_duration", app.Options{Network: nb, Target: tgt, Params: map[string]string{"seg_duration": "-1s"}}},
		{"zero start_threshold", app.Options{Network: nb, Target: tgt, Params: map[string]string{"start_threshold": "0s"}}},
		{"zero buffer_target", app.Options{Network: nb, Target: tgt, Params: map[string]string{"buffer_target": "0s"}}},
		{"zero rebuffer_target", app.Options{Network: nb, Target: tgt, Params: map[string]string{"rebuffer_target": "0s"}}},
		{"start above target", app.Options{Network: nb, Target: tgt, Params: map[string]string{"start_threshold": "5s", "buffer_target": "4s"}}},
		{"rebuffer above target", app.Options{Network: nb, Target: tgt, Params: map[string]string{"rebuffer_target": "5s", "buffer_target": "4s"}}},
		{"h2 without tls", app.Options{Network: nb, Target: tgt, Params: map[string]string{"h2": "true"}}},
		{"tls_ca without tls", app.Options{Network: nb, Target: tgt, Params: map[string]string{"tls_ca": "aGk="}}},
		{"malformed tls bool", app.Options{Network: nb, Target: tgt, Params: map[string]string{"tls": "sure"}}},
		{"malformed buffer_target", app.Options{Network: nb, Target: tgt, Params: map[string]string{"buffer_target": "soon"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewClient(tc.opts); err == nil {
				t.Fatal("Build succeeded, want error")
			}
		})
	}
}

// TestClientOnly: the app registers a client under "video" and no server —
// the far end is httpx's HTTPOrigin by design.
func TestClientOnly(t *testing.T) {
	_, nb := netpath.Memory()
	defer nb.Close()
	cli, err := app.ClientRegistry.Build(Name, app.Options{Network: nb, Target: "localhost:80"})
	if err != nil {
		t.Fatalf("ClientRegistry.Build(%q): %v", Name, err)
	}
	if cli.Name() != Name {
		t.Errorf("Name = %q, want %q", cli.Name(), Name)
	}
	if Name != metrics.KindVideo {
		t.Errorf("registry name %q != metrics.KindVideo %q", Name, metrics.KindVideo)
	}
	if _, err := app.ServerRegistry.Build(Name, app.Options{Network: nb}); err == nil {
		t.Error(`ServerRegistry.Build("video") succeeded, want unknown (client-only app)`)
	}
}

// TestRunOnce: Run may be called once; the second call fails fast.
func TestRunOnce(t *testing.T) {
	_, nb := netpath.Memory()
	defer nb.Close()
	cli, err := NewClient(app.Options{Network: nb, Target: "localhost:80"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c := cli.(*client)
	c.started.Store(true) // simulate a run in progress
	if err := c.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "already run") {
		t.Errorf("second Run = %v, want already-run error", err)
	}
}

// TestLadderExpectationCheck: the manifest is the truth; the expectation
// only asserts, matching on bitrate identity (labels are cosmetic).
func TestLadderExpectationCheck(t *testing.T) {
	ladder := []rendition{{kbps: 100}, {kbps: 1000}}
	match, err := httpx.ParseLadder("a:100k,b:1000k")
	if err != nil {
		t.Fatal(err)
	}
	if cerr := checkExpectation(match, ladder); cerr != nil {
		t.Errorf("matching expectation rejected: %v", cerr)
	}
	if cerr := checkExpectation(nil, ladder); cerr != nil {
		t.Errorf("absent expectation rejected: %v", cerr)
	}
	for _, bad := range []string{"a:100k", "a:100k,b:900k", "a:100k,b:1000k,c:2000k"} {
		expect, err := httpx.ParseLadder(bad)
		if err != nil {
			t.Fatal(err)
		}
		cerr := checkExpectation(expect, ladder)
		if !errors.Is(cerr, ErrLadderMismatch) {
			t.Errorf("expectation %q: err = %v, want ErrLadderMismatch", bad, cerr)
		}
	}
}
