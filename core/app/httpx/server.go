// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bgrewell/loom/core/accounting"
	"github.com/bgrewell/loom/core/app"
	"github.com/bgrewell/loom/core/metrics"
	"github.com/bgrewell/loom/core/netpath"
	"github.com/bgrewell/loom/core/units"
)

const (
	// defaultLadder is the origin's bitrate ladder when none is given:
	// label:rate pairs, rates in loom's rate grammar.
	defaultLadder = "240p:400k,480p:1200k,720p:2500k"
	// defaultSegDuration is the media segment duration.
	defaultSegDuration = 4 * time.Second
	// defaultSegments is the rendition playlist length (150 × 4 s = 10 min of
	// synthetic content, comfortably more than any phase demo plays).
	defaultSegments = 150
	// maxObjectBytes caps /object/{bytes} so a hostile or fat-fingered client
	// cannot make the origin stream unbounded gigabytes per request.
	maxObjectBytes = 1 << 30
	// bodyChunk is the deterministic body writer's buffer size.
	bodyChunk = 32 * 1024
)

// Rung is one ladder rendition. Kbps doubles as the path segment
// (/media/{name}/{kbps}/…), so it is the rendition's identity. Exported for
// core/app/vidstream, whose optional ladder-expectation parameter shares the
// origin's grammar.
type Rung struct {
	Label string
	Kbps  int
}

// origin is the "http" app's server side — HTTPOrigin, loom's own web/video
// far end: deterministic object bodies, generated HLS/DASH manifests and
// segments, optional self-signed TLS with h2.
type origin struct {
	ln      net.Listener
	srv     *http.Server
	addr    netip.AddrPort
	useTLS  bool
	certPEM []byte

	seed     int64
	ladder   []Rung
	segDur   time.Duration
	segments int

	rec       *recorder
	counters  accounting.Counters
	started   atomic.Bool
	closeOnce sync.Once
}

// NewServer builds the "http" origin, bound at build time through
// Options.Network so Addr is valid when the agent advertises data_port. See
// the package documentation for the parameter table (tls, h2, host, ladder,
// seg_duration, segments, port_min/port_max).
func NewServer(o app.Options) (app.Server, error) {
	if o.Network == nil {
		return nil, errors.New("httpx: Options.Network is required")
	}
	p := app.NewParams(o.Params)
	var (
		useTLS    = p.GetBool("tls", false)
		useH2     = p.GetBool("h2", false)
		host      = p.GetString("host", "")
		ladderStr = p.GetString("ladder", defaultLadder)
		segDur    = p.GetDuration("seg_duration", defaultSegDuration)
		segments  = p.GetInt("segments", defaultSegments)
		portMin   = p.GetInt("port_min", 0)
		portMax   = p.GetInt("port_max", 0)
	)
	errs := []error{p.Err()}
	if useH2 && !useTLS {
		errs = append(errs, errors.New(`param "h2": requires tls=true (h2c is not supported)`))
	}
	if segDur <= 0 {
		errs = append(errs, fmt.Errorf("param %q: must be positive, got %v", "seg_duration", segDur))
	}
	if segments <= 0 {
		errs = append(errs, fmt.Errorf("param %q: must be positive, got %d", "segments", segments))
	}
	ladder, lerr := ParseLadder(ladderStr)
	if lerr != nil {
		errs = append(errs, fmt.Errorf("param %q: %w", "ladder", lerr))
	}
	if portMax == 0 {
		portMax = portMin
	}
	if portMin < 0 || portMax > 65535 || portMax < portMin {
		errs = append(errs, fmt.Errorf("invalid port range %d..%d", portMin, portMax))
	}
	if portMax > 0 && portMin == 0 {
		// Half a range is a silent ephemeral bind outside the firewall's
		// pinhole — reject at Build time (the core/app/voip rule).
		errs = append(errs, fmt.Errorf("port_max %d given without port_min", portMax))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("httpx: %w", err)
	}

	ln, err := listenRange(o.Network, portMin, portMax)
	if err != nil {
		return nil, err
	}
	s := &origin{
		ln:       ln,
		addr:     addrPortOf(ln.Addr()),
		useTLS:   useTLS,
		seed:     o.Seed,
		ladder:   ladder,
		segDur:   segDur,
		segments: segments,
		rec:      newRecorder(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /object/{bytes}", s.handleObject)
	mux.HandleFunc("GET /media/{name}/manifest.m3u8", s.handleMaster)
	mux.HandleFunc("GET /media/{name}/manifest.mpd", s.handleMPD)
	mux.HandleFunc("GET /media/{name}/{kbps}/playlist.m3u8", s.handlePlaylist)
	mux.HandleFunc("GET /media/{name}/{kbps}/{seg}", s.handleSegment)
	s.srv = &http.Server{
		Handler:           s.counted(mux),
		ReadHeaderTimeout: 10 * time.Second,
		// TLS probes and mismatched clients are the metrics plane's business
		// (Errors), not stderr noise on a lab box driven by a remote agent.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	if useTLS {
		cert, certPEM, cerr := selfSigned(host, s.addr.Addr())
		if cerr != nil {
			_ = ln.Close()
			return nil, cerr
		}
		s.certPEM = certPEM
		s.srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	// Explicit protocol set: HTTP/1.1 always; HTTP/2 only when asked for and
	// only over TLS (UnencryptedHTTP2 stays off — h2c is not supported).
	protos := new(http.Protocols)
	protos.SetHTTP1(true)
	protos.SetHTTP2(useTLS && useH2)
	s.srv.Protocols = protos
	return s, nil
}

// listenRange binds the first free port in [portMin, portMax] (both zero =
// ephemeral), for firewall determinism like the voip answerer.
func listenRange(n netpath.Network, portMin, portMax int) (net.Listener, error) {
	if portMin == 0 {
		ln, err := n.Listen("tcp", ":0")
		if err != nil {
			return nil, fmt.Errorf("httpx: bind: %w", err)
		}
		return ln, nil
	}
	var lastErr error
	for port := portMin; port <= portMax; port++ {
		ln, err := n.Listen("tcp", net.JoinHostPort("", strconv.Itoa(port)))
		if err == nil {
			return ln, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("httpx: no free port in %d..%d: %w", portMin, portMax, lastErr)
}

// ParseLadder parses "label:rate" pairs ("240p:400k,480p:1200k"). Rates use
// loom's rate grammar and must round to at least 1 kbps, because the kbps
// value is the rendition's path segment. The grammar is shared with the
// "video" app's ladder-expectation parameter (core/app/vidstream).
func ParseLadder(s string) ([]Rung, error) {
	var ladder []Rung
	seen := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		label, rateStr, ok := strings.Cut(part, ":")
		if !ok || strings.TrimSpace(label) == "" {
			return nil, fmt.Errorf("rung %q: want label:rate", part)
		}
		rate, err := units.ParseRate(rateStr)
		if err != nil {
			return nil, fmt.Errorf("rung %q: %w", part, err)
		}
		kbps := int(rate / 1000)
		if kbps < 1 {
			return nil, fmt.Errorf("rung %q: rate below 1 kbps", part)
		}
		if seen[kbps] {
			return nil, fmt.Errorf("rung %q: duplicate %d kbps rendition", part, kbps)
		}
		seen[kbps] = true
		ladder = append(ladder, Rung{Label: strings.TrimSpace(label), Kbps: kbps})
	}
	if len(ladder) == 0 {
		return nil, errors.New("empty ladder")
	}
	return ladder, nil
}

// Name implements app.Server.
func (s *origin) Name() string { return Name }

// Counters implements app.Server: one "packet" per response, response-body
// bytes.
func (s *origin) Counters() *accounting.Counters { return &s.counters }

// Addr implements app.Server: the bound address, valid from build time (the
// data_port readback pattern). Networks whose addresses are not IP-formed
// (the in-memory fabric) report the unspecified IPv4 address with the real
// port.
func (s *origin) Addr() netip.AddrPort { return s.addr }

// CertificatePEM returns the origin's self-signed serving certificate in PEM
// form (nil when TLS is off) — the optional capability an embedder or
// controller reads to hand clients a tls_ca pin, keeping certificate
// verification on end to end.
func (s *origin) CertificatePEM() []byte {
	if s.certPEM == nil {
		return nil
	}
	return append([]byte(nil), s.certPEM...)
}

// Metrics implements metrics.Source: served-request counts, error count and
// goodput for the observation window (the origin measures no client-side
// latencies, so the timing percentiles stay zero).
func (s *origin) Metrics() metrics.Snapshot { return s.rec.Metrics() }

// CumulativeMetrics returns the whole-run snapshot without closing an
// observation interval.
func (s *origin) CumulativeMetrics() metrics.Snapshot { return s.rec.Cumulative() }

// Close implements io.Closer for the built-but-never-run teardown path: it
// releases the advertised port and any running server. Idempotent.
func (s *origin) Close() error {
	s.closeOnce.Do(func() {
		_ = s.srv.Close()
		_ = s.ln.Close()
	})
	return nil
}

// Run implements app.Server: serve until ctx is cancelled (flow duration —
// app-server flows are always duration-bounded, the orphan-protection rule).
// Returns nil on cancellation; Run may be called once.
func (s *origin) Run(ctx context.Context) error {
	if !s.started.CompareAndSwap(false, true) {
		return errors.New("httpx: origin already run")
	}
	s.rec.runStarted()
	defer s.rec.runStopped()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = s.srv.Close()
		case <-done:
		}
	}()

	var err error
	if s.useTLS {
		err = s.srv.ServeTLS(s.ln, "", "")
	} else {
		err = s.srv.Serve(s.ln)
	}
	if errors.Is(err, http.ErrServerClosed) || ctx.Err() != nil {
		return nil
	}
	return err
}

// counted wraps the mux with per-response accounting: bytes and one "packet"
// per response into Counters, request/error/goodput samples into the
// recorder.
func (s *origin) counted(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cw := &countingWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(cw, r)
		s.counters.Add(cw.n)
		// A transfer whose body write failed (client gone, flow stopped
		// mid-request) is not a served response: without cw.failed it would be
		// recorded as a clean 200 with partial bytes, making the origin's
		// error picture at teardown rosier than the wire.
		s.rec.observe(sample{Bytes: cw.n, Proto: r.Proto, Err: cw.status >= 400 || cw.failed})
	})
}

// countingWriter counts body bytes, captures the status code, and remembers
// whether any body write failed (aborted transfer).
type countingWriter struct {
	http.ResponseWriter
	n      uint64
	status int
	failed bool
}

// WriteHeader implements http.ResponseWriter.
func (w *countingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Write implements http.ResponseWriter.
func (w *countingWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.n += uint64(n)
	if err != nil {
		w.failed = true
	}
	return n, err
}

// handleObject serves GET /object/{bytes}: a pseudo-random body of exactly
// {bytes} bytes, deterministic in (server seed, path), Content-Length set.
func (s *origin) handleObject(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.ParseInt(r.PathValue("bytes"), 10, 64)
	if err != nil || n < 0 || n > maxObjectBytes {
		http.Error(w, fmt.Sprintf("object size must be 0..%d bytes", maxObjectBytes), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	s.writeBody(w, r, n)
}

// handleSegment serves GET /media/{name}/{kbps}/seg{n}: a deterministic body
// of kbps·seg_duration/8 bytes. Unknown renditions and segments beyond the
// playlist 404, so clients exercise exactly the ladder the manifests declare.
func (s *origin) handleSegment(w http.ResponseWriter, r *http.Request) {
	if !s.rungExists(r.PathValue("kbps")) {
		http.NotFound(w, r)
		return
	}
	segName, ok := strings.CutPrefix(r.PathValue("seg"), "seg")
	if !ok {
		http.NotFound(w, r)
		return
	}
	segN, err := strconv.Atoi(segName)
	if err != nil || segN < 0 || segN >= s.segments {
		http.NotFound(w, r)
		return
	}
	kbps, _ := strconv.Atoi(r.PathValue("kbps"))
	size := int64(kbps) * int64(s.segDur/time.Millisecond) / 8 // kbit/s × ms = bits; /8 = bytes
	w.Header().Set("Content-Type", "video/mp2t")
	s.writeBody(w, r, size)
}

// rungExists reports whether the path's kbps segment names a ladder rung.
func (s *origin) rungExists(kbpsStr string) bool {
	kbps, err := strconv.Atoi(kbpsStr)
	if err != nil {
		return false
	}
	for _, rg := range s.ladder {
		if rg.Kbps == kbps {
			return true
		}
	}
	return false
}

// handleMaster serves GET /media/{name}/manifest.m3u8: the HLS master
// playlist, one variant per ladder rung pointing at the rung's media
// playlist.
func (s *origin) handleMaster(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n")
	for _, rg := range s.ladder {
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,NAME=%q\n%d/playlist.m3u8\n", rg.Kbps*1000, rg.Label, rg.Kbps)
	}
	writeGenerated(w, r, "application/vnd.apple.mpegurl", b.String())
}

// writeGenerated sends a generated text body with Content-Type/Length set,
// skipping the body for HEAD (net/http would discard it after countingWriter
// counted phantom bytes).
func writeGenerated(w http.ResponseWriter, r *http.Request, contentType, body string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.WriteString(w, body)
}

// handlePlaylist serves GET /media/{name}/{kbps}/playlist.m3u8: the rung's
// VOD media playlist, segments seg0..seg{segments−1} of seg_duration each.
func (s *origin) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	if !s.rungExists(r.PathValue("kbps")) {
		http.NotFound(w, r)
		return
	}
	segSec := s.segDur.Seconds()
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n",
		int64(segSec+0.999))
	for i := 0; i < s.segments; i++ {
		fmt.Fprintf(&b, "#EXTINF:%.3f,\nseg%d\n", segSec, i)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	writeGenerated(w, r, "application/vnd.apple.mpegurl", b.String())
}

// handleMPD serves GET /media/{name}/manifest.mpd: a minimal static DASH MPD
// for the same ladder, SegmentTemplate media matching the segment endpoint.
func (s *origin) handleMPD(w http.ResponseWriter, r *http.Request) {
	total := s.segDur.Seconds() * float64(s.segments)
	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" profiles="urn:mpeg:dash:profile:isoff-main:2011" mediaPresentationDuration="PT%.3fS" minBufferTime="PT%.3fS">
  <Period>
    <AdaptationSet mimeType="video/mp2t" contentType="video">
      <SegmentTemplate media="$RepresentationID$/seg$Number$" timescale="1000" duration="%d" startNumber="0"/>
`, total, s.segDur.Seconds(), s.segDur.Milliseconds())
	for _, rg := range s.ladder {
		fmt.Fprintf(&b, "      <Representation id=%q bandwidth=%q/>\n",
			strconv.Itoa(rg.Kbps), strconv.Itoa(rg.Kbps*1000))
	}
	b.WriteString("    </AdaptationSet>\n  </Period>\n</MPD>\n")
	writeGenerated(w, r, "application/dash+xml", b.String())
}

// writeBody streams a deterministic pseudo-random body of exactly n bytes:
// the same (server seed, request path) always yields the same bytes, so
// repeated fetches and cross-host comparisons are stable. Content-Length is
// always set. HEAD requests (which "GET /..." mux patterns also match) get
// headers only: net/http would silently discard the body writes while
// countingWriter counted them, so generating it would burn up to 1 GiB of
// splitmix64 per probe and credit goodput for bytes that never hit the wire.
func (s *origin) writeBody(w http.ResponseWriter, r *http.Request, n int64) {
	w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
	if r.Method == http.MethodHead {
		return
	}
	state := bodySeed(s.seed, r.URL.Path)
	buf := make([]byte, bodyChunk)
	for n > 0 {
		chunk := int64(len(buf))
		if n < chunk {
			chunk = n
		}
		fillDeterministic(buf[:chunk], &state)
		if _, err := w.Write(buf[:chunk]); err != nil {
			return // client went away mid-body
		}
		n -= chunk
	}
}

// bodySeed derives the deterministic body generator's initial state from the
// server seed and the request path.
func bodySeed(seed int64, path string) uint64 {
	h := fnv.New64a()
	_, _ = io.WriteString(h, path)
	return h.Sum64() ^ uint64(seed)
}

// fillDeterministic fills b from a splitmix64 stream, advancing state.
// splitmix64 passes BigCrush, needs no tables, and — unlike math/rand
// sources — is trivially re-derivable by any other implementation that wants
// to verify a body byte-for-byte.
func fillDeterministic(b []byte, state *uint64) {
	var word [8]byte
	for i := 0; i < len(b); i += 8 {
		*state += 0x9e3779b97f4a7c15
		z := *state
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		z ^= z >> 31
		binary.LittleEndian.PutUint64(word[:], z)
		copy(b[i:], word[:])
	}
}

// addrPortOf extracts a netip.AddrPort from a net.Addr, degrading to the
// unspecified IPv4 address for networks whose address strings are not
// IP-formed (the in-memory fabric's "mem:port") — the same convention as
// core/app/voip.
func addrPortOf(a net.Addr) netip.AddrPort {
	if ta, ok := a.(*net.TCPAddr); ok {
		return ta.AddrPort()
	}
	host, ps, err := net.SplitHostPort(a.String())
	if err != nil {
		return netip.AddrPort{}
	}
	port, err := strconv.Atoi(ps)
	if err != nil || port < 0 || port > 65535 {
		return netip.AddrPort{}
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		ip = netip.IPv4Unspecified()
	}
	return netip.AddrPortFrom(ip, uint16(port))
}
