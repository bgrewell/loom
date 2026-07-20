// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package vidstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bgrewell/loom/core/accounting"
	"github.com/bgrewell/loom/core/app"
	"github.com/bgrewell/loom/core/app/httpx"
	"github.com/bgrewell/loom/core/rtp"
)

const (
	// Buffer-model defaults (seconds of media).
	defaultStartThreshold = 2 * time.Second
	defaultBufferTarget   = 12 * time.Second
	defaultRebufferTarget = 4 * time.Second
	// abrThroughput and abrBuffer are the ABR policy names.
	abrThroughput = "throughput"
	abrBuffer     = "buffer"
	// throughputSafety is the fraction of the throughput estimate a rung's
	// bitrate must fit within — the standard headroom against estimate noise.
	throughputSafety = 0.8
	// ewmaAlpha is the weight of the newest per-segment throughput sample in
	// the harmonic-domain EWMA (see estObserve).
	ewmaAlpha = 0.3
	// errPause is the minimum retry pause after a failed fetch, so a dead
	// origin costs one retry per errPause instead of a hot spin (the httpx
	// client discipline).
	errPause = 100 * time.Millisecond
	// minFetchElapsed floors the measured segment fetch time so a segment
	// served faster than the clock resolution yields a finite (huge, which is
	// the honest reading) throughput sample instead of a division by zero.
	minFetchElapsed = 50 * time.Microsecond
)

// ErrLadderMismatch is returned (wrapped) by Run when the optional ladder
// expectation parameter disagrees with the rung set the origin's manifest
// declares — the manifest is the truth, the parameter only an assertion.
var ErrLadderMismatch = errors.New("vidstream: origin ladder does not match the ladder expectation")

// playState is the virtual player's playback state.
type playState int

const (
	// stateBuffering: before first play — filling toward start_threshold.
	stateBuffering playState = iota
	// statePlaying: playhead advancing, buffer draining in real time.
	statePlaying
	// stateStalled: buffer hit zero mid-stream; playhead paused.
	stateStalled
	// stateEnded: end of stream reached and the buffer played out.
	stateEnded
)

// client is the "video" app: the ABR player. All playback accounting is
// event-driven with lazy real-time drain: syncLocked advances the virtual
// playhead to "now" on every state access, so stall onsets are computed at
// their exact instant (lastSync + remaining buffer) rather than quantized to
// a polling tick.
type client struct {
	hc           *http.Client
	tr           *http.Transport
	baseURL      string
	hostOverride string

	mediaName      string
	expect         []httpx.Rung // optional ladder expectation; nil = none
	segDurOverride time.Duration
	startThreshold time.Duration
	bufferTarget   time.Duration
	rebufferTarget time.Duration
	policy         string

	// now is the playhead clock, injectable for deterministic model tests.
	now func() time.Time

	counters accounting.Counters
	started  atomic.Bool

	mu sync.Mutex
	// Ladder and ABR state.
	ladder  []rendition
	rungIdx int
	estInv  float64 // harmonic-domain EWMA state: EWMA of 1/kbps
	estOK   bool
	// Playback state (virtual buffer + playhead).
	state       playState
	frozen      bool // Run has returned: the playhead is parked, no more accrual
	runStart    time.Time
	lastSync    time.Time
	buffer      time.Duration
	endOfStream bool
	startup     time.Duration
	startupSet  bool
	playTime    time.Duration
	stallTime   time.Duration
	stallStart  time.Time
	// Cumulative QoE accounting.
	stalls   uint64
	segments uint64
	upSw     uint64
	downSw   uint64
	events   []rtp.Gap // completed stalls
	kbpsDur  float64   // Σ rung kbps × segment seconds (bitrate numerator)
	durSum   float64   // Σ segment seconds (bitrate denominator)
	// Window marks: values at the previous Metrics call.
	mkSegments, mkStalls, mkUp, mkDown uint64
	mkStallTime, mkPlayTime            time.Duration
	mkEvents                           int
	mkKbpsDur, mkDurSum                float64
}

// NewClient builds the "video" player aimed at Options.Target (the
// HTTPOrigin's host:port). See the package documentation for the parameter
// table; the transport (tls/h2/host/tls_ca/tls_insecure) is httpx's shared
// grammar via httpx.NewTransport.
func NewClient(o app.Options) (app.Client, error) {
	if o.Network == nil {
		return nil, errors.New("vidstream: Options.Network is required")
	}
	if o.Target == "" {
		return nil, errors.New("vidstream: client requires Options.Target (origin host:port)")
	}
	p := app.NewParams(o.Params)
	var (
		mediaName = p.GetString("url_name", "stream")
		ladderStr = p.GetString("ladder", "")
		segDur    = p.GetDuration("seg_duration", 0)
		startThr  = p.GetDuration("start_threshold", defaultStartThreshold)
		bufTarget = p.GetDuration("buffer_target", defaultBufferTarget)
		rebufTgt  = p.GetDuration("rebuffer_target", defaultRebufferTarget)
		policy    = p.GetString("abr", abrThroughput)
	)
	tr, scheme, host, terr := httpx.NewTransport(o.Network, p)
	errs := []error{terr}
	if strings.Contains(mediaName, "/") {
		errs = append(errs, fmt.Errorf("param %q: must not contain '/', got %q", "url_name", mediaName))
	}
	var expect []httpx.Rung
	if ladderStr != "" {
		var lerr error
		if expect, lerr = httpx.ParseLadder(ladderStr); lerr != nil {
			errs = append(errs, fmt.Errorf("param %q: %w", "ladder", lerr))
		}
	}
	if segDur < 0 {
		errs = append(errs, fmt.Errorf("param %q: must not be negative, got %v", "seg_duration", segDur))
	}
	if startThr <= 0 {
		errs = append(errs, fmt.Errorf("param %q: must be positive, got %v", "start_threshold", startThr))
	}
	if bufTarget <= 0 {
		errs = append(errs, fmt.Errorf("param %q: must be positive, got %v", "buffer_target", bufTarget))
	}
	if rebufTgt <= 0 {
		errs = append(errs, fmt.Errorf("param %q: must be positive, got %v", "rebuffer_target", rebufTgt))
	}
	if startThr > 0 && bufTarget > 0 && startThr > bufTarget {
		errs = append(errs, fmt.Errorf("param %q: %v exceeds buffer_target %v", "start_threshold", startThr, bufTarget))
	}
	if rebufTgt > 0 && bufTarget > 0 && rebufTgt > bufTarget {
		errs = append(errs, fmt.Errorf("param %q: %v exceeds buffer_target %v", "rebuffer_target", rebufTgt, bufTarget))
	}
	if policy != abrThroughput && policy != abrBuffer {
		errs = append(errs, fmt.Errorf("param %q: want %q or %q, got %q", "abr", abrThroughput, abrBuffer, policy))
	}
	errs = append(errs, p.Err())
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("vidstream: %w", err)
	}
	return &client{
		hc:             &http.Client{Transport: tr},
		tr:             tr,
		baseURL:        scheme + "://" + o.Target,
		hostOverride:   host,
		mediaName:      mediaName,
		expect:         expect,
		segDurOverride: segDur,
		startThreshold: startThr,
		bufferTarget:   bufTarget,
		rebufferTarget: rebufTgt,
		policy:         policy,
		now:            time.Now,
	}, nil
}

// Name implements app.Client.
func (c *client) Name() string { return Name }

// Counters implements app.Client: one "packet" per completed request
// (manifest, playlist, segment), response-body bytes.
func (c *client) Counters() *accounting.Counters { return &c.counters }

// Close implements io.Closer for the built-but-never-run teardown path,
// releasing the transport's idle connections. Idempotent.
func (c *client) Close() error {
	c.tr.CloseIdleConnections()
	return nil
}

// Run implements app.Client: play the stream once — manifest, then segments
// under the buffer model — bounded by ctx (the flow duration). Transient
// fetch failures are retried (a lossy path is a measurement, visible as
// stalls, not a flow failure); a malformed manifest or a ladder-expectation
// mismatch is a configuration failure and returns an error. Run may be
// called once; it returns nil on clean completion or cancellation.
func (c *client) Run(ctx context.Context) error {
	if !c.started.CompareAndSwap(false, true) {
		return errors.New("vidstream: client already run")
	}
	defer c.tr.CloseIdleConnections()
	now := c.now()
	c.mu.Lock()
	c.runStart, c.lastSync = now, now
	c.mu.Unlock()
	// Every exit path — natural completion, ctx cancellation (the flow
	// duration expiring mid-stall is the exact case this app measures), or a
	// configuration error — freezes the playhead, so post-run Metrics/
	// CumulativeMetrics reads are stable instead of accruing wall time
	// forever.
	defer c.freeze()

	if err := c.loadManifest(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	return c.play(ctx)
}

// loadManifest fetches and parses the master manifest, verifies the optional
// ladder expectation, and loads the lowest rung's media playlist (the
// player's conservative starting rendition).
func (c *client) loadManifest(ctx context.Context) error {
	masterURL, err := url.Parse(c.baseURL + "/media/" + url.PathEscape(c.mediaName) + "/manifest.m3u8")
	if err != nil {
		return fmt.Errorf("vidstream: manifest url: %w", err)
	}
	body, ok := c.fetchTextRetry(ctx, masterURL)
	if !ok {
		return ctx.Err()
	}
	ladder, err := parseMaster(masterURL, body)
	if err != nil {
		return err
	}
	if err := checkExpectation(c.expect, ladder); err != nil {
		return err
	}
	c.mu.Lock()
	c.ladder = ladder
	c.rungIdx = 0
	c.mu.Unlock()
	return c.ensurePlaylist(ctx, 0)
}

// checkExpectation compares the manifest's rung set against the optional
// ladder expectation by bitrate identity (kbps — labels are cosmetic).
func checkExpectation(expect []httpx.Rung, ladder []rendition) error {
	if expect == nil {
		return nil
	}
	want := make(map[int]bool, len(expect))
	for _, r := range expect {
		want[r.Kbps] = true
	}
	got := make([]int, 0, len(ladder))
	for _, r := range ladder {
		got = append(got, r.kbps)
	}
	ok := len(want) == len(ladder)
	for _, kbps := range got {
		if !want[kbps] {
			ok = false
		}
	}
	if !ok {
		return fmt.Errorf("%w: manifest declares %v kbps, expectation %d rungs", ErrLadderMismatch, got, len(want))
	}
	return nil
}

// ensurePlaylist loads rung idx's media playlist if not already cached
// (players fetch a rendition's playlist on first switch to it), applying the
// seg_duration override. Transient fetch failures are retried until ctx
// ends; a malformed playlist is an error.
func (c *client) ensurePlaylist(ctx context.Context, idx int) error {
	c.mu.Lock()
	r := c.ladder[idx]
	c.mu.Unlock()
	if r.segs != nil {
		return nil
	}
	body, ok := c.fetchTextRetry(ctx, r.playlist)
	if !ok {
		return ctx.Err()
	}
	segs, err := parseMedia(r.playlist, body)
	if err != nil {
		return err
	}
	if c.segDurOverride > 0 {
		for i := range segs {
			segs[i].dur = c.segDurOverride
		}
	}
	c.mu.Lock()
	c.ladder[idx].segs = segs
	c.mu.Unlock()
	return nil
}

// play runs the segment loop under the buffer model, then plays out the tail.
func (c *client) play(ctx context.Context) error {
	for i := 0; ; i++ {
		// End of stream: the current rendition has no segment i, so there is
		// nothing left to pace, decide, or fetch. Checked before the ABR
		// decision — deciding here would count a switch (and possibly fetch a
		// playlist) for a segment that is never downloaded.
		c.mu.Lock()
		total := len(c.ladder[c.rungIdx].segs)
		c.mu.Unlock()
		if i >= total {
			break
		}
		// Pace: fetch only once the buffer has drained below the target.
		for {
			c.mu.Lock()
			c.syncLocked(c.now())
			wait := c.buffer - c.bufferTarget
			c.mu.Unlock()
			if wait <= 0 {
				break
			}
			if !sleepCtx(ctx, wait) {
				return nil
			}
		}
		// Decide the rendition for this segment (ABR), then make sure its
		// playlist is loaded.
		c.mu.Lock()
		c.syncLocked(c.now())
		c.decideLocked()
		idx := c.rungIdx
		c.mu.Unlock()
		if err := c.ensurePlaylist(ctx, idx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		c.mu.Lock()
		r := c.ladder[idx]
		c.mu.Unlock()
		if i >= len(r.segs) {
			break // the decided rung's playlist is shorter — end of stream
		}
		seg := r.segs[i]
		// Fetch the segment, retrying transient failures: the buffer keeps
		// draining meanwhile, so a lossy path shows up as stalls.
		for {
			bytes, elapsed, err := c.fetchDiscard(ctx, seg.url)
			if err == nil {
				c.recordSegment(r.kbps, seg.dur, bytes, elapsed)
				break
			}
			if ctx.Err() != nil {
				return nil
			}
			if !sleepCtx(ctx, errPause) {
				return nil
			}
		}
	}
	c.noMoreSegments()
	// Play out the remaining buffer in real time.
	for {
		c.mu.Lock()
		c.syncLocked(c.now())
		st, buf := c.state, c.buffer
		c.mu.Unlock()
		if st == stateEnded || buf <= 0 {
			return nil
		}
		if !sleepCtx(ctx, buf) {
			return nil
		}
	}
}

// freeze parks the playhead at the run's end: it syncs to now (so time up to
// the exit instant is accounted), closes a stall still in progress as a
// timestamped event ending at that instant (the stall most likely to matter —
// one overlapping the flow's end — must not vanish from the correlation
// timeline), and stops all further accrual, so post-run Metrics and
// CumulativeMetrics reads are stable. Run defers it on every exit path.
func (c *client) freeze() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.syncLocked(now)
	if c.state == stateStalled {
		c.events = append(c.events, rtp.Gap{Start: c.stallStart, End: now})
	}
	c.frozen = true
}

// syncLocked advances the virtual playhead to now: while playing, wall time
// drains the buffer 1:1 (into playTime); when the buffer runs out
// mid-stream the player stalls at the exact exhaustion instant; at end of
// stream, exhaustion is completion. While stalled, wall time accrues to
// stallTime. After freeze (Run has returned) the playhead is parked and this
// is a no-op. Callers hold mu.
func (c *client) syncLocked(now time.Time) {
	if c.frozen {
		return
	}
	if now.Before(c.lastSync) {
		now = c.lastSync
	}
	switch c.state {
	case statePlaying:
		elapsed := now.Sub(c.lastSync)
		if elapsed < c.buffer {
			c.buffer -= elapsed
			c.playTime += elapsed
		} else {
			c.playTime += c.buffer
			exhausted := c.lastSync.Add(c.buffer)
			c.buffer = 0
			if c.endOfStream {
				c.state = stateEnded
			} else {
				c.state = stateStalled
				c.stallStart = exhausted
				c.stalls++
				c.stallTime += now.Sub(exhausted)
			}
		}
	case stateStalled:
		c.stallTime += now.Sub(c.lastSync)
	}
	c.lastSync = now
}

// recordSegment accounts one downloaded segment: buffer growth, bitrate
// history, the throughput EWMA, and the buffering→playing (startup) and
// stalled→playing (rebuffered) transitions.
func (c *client) recordSegment(kbps int, dur time.Duration, bytes uint64, elapsed time.Duration) {
	if elapsed < minFetchElapsed {
		elapsed = minFetchElapsed
	}
	sample := float64(bytes) * 8 / 1000 / elapsed.Seconds() // kbit/s
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.syncLocked(now)
	c.estObserveLocked(sample)
	c.buffer += dur
	c.segments++
	c.kbpsDur += float64(kbps) * dur.Seconds()
	c.durSum += dur.Seconds()
	switch c.state {
	case stateBuffering:
		if c.buffer >= c.startThreshold {
			c.state = statePlaying
			c.startup = now.Sub(c.runStart)
			c.startupSet = true
		}
	case stateStalled:
		if c.buffer >= c.rebufferTarget {
			c.events = append(c.events, rtp.Gap{Start: c.stallStart, End: now})
			c.state = statePlaying
		}
	}
}

// noMoreSegments marks end of stream: a player still filling toward the
// start threshold plays what it has (nothing more is coming), and a stall in
// progress ends — the remaining buffer plays out and running dry is
// completion, not a stall.
func (c *client) noMoreSegments() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.syncLocked(now)
	c.endOfStream = true
	switch c.state {
	case stateBuffering:
		c.state = statePlaying
		c.startup = now.Sub(c.runStart)
		c.startupSet = true
	case stateStalled:
		c.events = append(c.events, rtp.Gap{Start: c.stallStart, End: now})
		c.state = statePlaying
	}
	if c.buffer == 0 {
		c.state = stateEnded
	}
}

// estObserveLocked folds one throughput sample (kbit/s) into the
// harmonic-domain EWMA: the average is kept over 1/throughput (seconds per
// kilobit), so the estimate tracks a bandwidth collapse within a segment or
// two even after an arbitrarily fast phase — the plain arithmetic EWMA would
// need dozens of samples to come down from a multi-Gbps memory-fabric
// reading. Callers hold mu.
func (c *client) estObserveLocked(kbps float64) {
	if kbps <= 0 {
		return
	}
	inv := 1 / kbps
	if !c.estOK {
		c.estInv, c.estOK = inv, true
		return
	}
	c.estInv = (1-ewmaAlpha)*c.estInv + ewmaAlpha*inv
}

// decideLocked runs the ABR policy and accounts a ladder switch. Callers
// hold mu, with the playhead already synced (the buffer policy reads the
// current level).
//
// Neither policy applies hysteresis (no switch-up margin, dwell time, or
// band overlap) — a deliberate model simplification, documented in the
// package docs: an estimate or buffer level hovering at a rung boundary can
// oscillate between adjacent rungs, so RepSwitchesUp/Down measure the raw
// policy decisions, not debounced player behavior.
func (c *client) decideLocked() {
	var next int
	switch c.policy {
	case abrBuffer:
		next = pickBuffer(len(c.ladder), c.buffer, c.rebufferTarget, c.bufferTarget)
	default:
		if !c.estOK {
			return // no sample yet: stay on the conservative starting rung
		}
		next = pickThroughput(c.ladder, 1/c.estInv)
	}
	if next > c.rungIdx {
		c.upSw++
	} else if next < c.rungIdx {
		c.downSw++
	}
	c.rungIdx = next
}

// pickThroughput returns the highest rung whose bitrate fits within the
// safety fraction of the throughput estimate (kbit/s); the lowest rung when
// none fits. ladder is sorted ascending.
func pickThroughput(ladder []rendition, estKbps float64) int {
	idx := 0
	for i, r := range ladder {
		if float64(r.kbps) <= estKbps*throughputSafety {
			idx = i
		}
	}
	return idx
}

// pickBuffer maps the buffer level onto the ladder (BBA-style thresholds):
// lowest rung at or below the reservoir (rebuffer_target), highest at the
// cushion (buffer_target), linear steps in between.
func pickBuffer(n int, buffer, reservoir, cushion time.Duration) int {
	if n <= 1 || buffer <= reservoir {
		return 0
	}
	if buffer >= cushion || cushion <= reservoir {
		return n - 1
	}
	frac := float64(buffer-reservoir) / float64(cushion-reservoir)
	idx := int(frac * float64(n))
	if idx > n-1 {
		idx = n - 1
	}
	return idx
}

// fetchTextRetry fetches u's body as text, retrying transient failures every
// errPause until ctx ends (ok=false). Used for manifests and playlists.
func (c *client) fetchTextRetry(ctx context.Context, u *url.URL) (string, bool) {
	for {
		body, err := c.fetchText(ctx, u)
		if err == nil {
			return body, true
		}
		if ctx.Err() != nil {
			return "", false
		}
		if !sleepCtx(ctx, errPause) {
			return "", false
		}
	}
}

// fetchText issues one GET and returns the whole body.
func (c *client) fetchText(ctx context.Context, u *url.URL) (string, error) {
	resp, err := c.get(ctx, u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	c.counters.Add(uint64(len(body)))
	return string(body), nil
}

// fetchDiscard issues one GET and discards the body, returning the body size
// and the full-request wall time (request start to last body byte) — the
// throughput sample an ABR player actually experiences.
func (c *client) fetchDiscard(ctx context.Context, u *url.URL) (uint64, time.Duration, error) {
	start := time.Now()
	resp, err := c.get(ctx, u)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return 0, 0, err
	}
	c.counters.Add(uint64(n))
	return uint64(n), time.Since(start), nil
}

// get issues one GET with the Host override applied, mapping non-2xx
// statuses to errors (the body is drained so the connection can be reused).
func (c *client) get(ctx context.Context, u *url.URL) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if c.hostOverride != "" {
		req.Host = c.hostOverride
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("vidstream: GET %s: status %s", u.Path, resp.Status)
	}
	return resp, nil
}

// sleepCtx sleeps for d or until ctx ends, reporting whether the full sleep
// completed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
