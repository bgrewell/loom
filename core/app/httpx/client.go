// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bgrewell/loom/core/accounting"
	"github.com/bgrewell/loom/core/app"
	"github.com/bgrewell/loom/core/emul"
	"github.com/bgrewell/loom/core/metrics"
	"github.com/bgrewell/loom/core/units"
)

// errPause is the minimum inter-request pause after a failed request when the
// think distribution sampled (near) zero, so a dead origin costs retries per
// errPause instead of a hot spin for the rest of the flow.
const errPause = 100 * time.Millisecond

// client is the "http" app's initiating side: a real net/http client whose
// Transport dials through the injected netpath.Network, fetching objects in a
// think-time loop bounded by the flow duration (ctx) or an object count.
type client struct {
	hc *http.Client
	tr *http.Transport

	baseURL      string // scheme://target
	hostOverride string // Host header + SNI, when set
	urlPath      string // fixed path; "" = draw from objectSize
	objectSize   emul.Dist
	think        emul.Dist
	objects      int
	seed         int64

	rec      *recorder
	counters accounting.Counters
	started  atomic.Bool
}

// NewClient builds the "http" client aimed at Options.Target (host:port). See
// the package documentation for the full parameter table (url_path, objects,
// object_size, think, tls, h2, host, tls_ca, tls_insecure). Certificate
// verification is on by default for TLS: pin the origin's cert via tls_ca
// (base64 PEM); tls_insecure is an explicit lab-only opt-out.
func NewClient(o app.Options) (app.Client, error) {
	if o.Network == nil {
		return nil, errors.New("httpx: Options.Network is required")
	}
	if o.Target == "" {
		return nil, errors.New("httpx: client requires Options.Target (origin host:port)")
	}
	p := app.NewParams(o.Params)
	var (
		urlPath  = p.GetString("url_path", "")
		objects  = p.GetInt("objects", 0)
		sizeStr  = p.GetString("object_size", "100KB")
		thinkStr = p.GetString("think", "")
		useTLS   = p.GetBool("tls", false)
		useH2    = p.GetBool("h2", false)
		host     = p.GetString("host", "")
		caB64    = p.GetString("tls_ca", "")
		insecure = p.GetBool("tls_insecure", false)
	)
	errs := []error{p.Err()}
	if urlPath != "" && !strings.HasPrefix(urlPath, "/") {
		errs = append(errs, fmt.Errorf("param %q: must begin with '/', got %q", "url_path", urlPath))
	}
	if objects < 0 {
		errs = append(errs, fmt.Errorf("param %q: must be >= 0, got %d", "objects", objects))
	}
	sizeDist, err := emul.SizeDist(sizeStr)
	if err != nil {
		errs = append(errs, fmt.Errorf("param %q: %w", "object_size", err))
	} else if r, rerr := units.ParseSizeRange(sizeStr); rerr == nil && r.Hi > maxObjectBytes {
		// The origin caps /object/{bytes} at maxObjectBytes; a draw above it
		// can only ever be refused with 400, so the flow would run at 100%
		// errors — a Build-time mistake, not a runtime measurement.
		errs = append(errs, fmt.Errorf("param %q: %s exceeds the origin's /object cap of %d bytes", "object_size", sizeStr, int64(maxObjectBytes)))
	}
	thinkDist := emul.Constant(0)
	if thinkStr != "" {
		if thinkDist, err = emul.DurationDist(thinkStr); err != nil {
			errs = append(errs, fmt.Errorf("param %q: %w", "think", err))
		} else if r, rerr := units.ParseDurationRange(thinkStr); rerr == nil && r.Lo < 0 {
			// Dist.Sample would silently clamp negatives to 0; reject like the
			// other negative knobs instead.
			errs = append(errs, fmt.Errorf("param %q: must not be negative, got %q", "think", thinkStr))
		}
	}
	if useH2 && !useTLS {
		errs = append(errs, errors.New(`param "h2": requires tls=true (h2c is not supported)`))
	}
	if !useTLS && (caB64 != "" || insecure) {
		errs = append(errs, errors.New(`params "tls_ca"/"tls_insecure": only meaningful with tls=true`))
	}
	var tcfg *tls.Config
	if useTLS {
		tcfg = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
		if caB64 != "" {
			pool, cerr := rootsFromParam(caB64)
			if cerr != nil {
				errs = append(errs, cerr)
			}
			tcfg.RootCAs = pool
		}
		if insecure {
			// Lab shortcut ONLY, explicitly opted into via tls_insecure:
			// verification is otherwise always on (pin via tls_ca).
			tcfg.InsecureSkipVerify = true
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("httpx: %w", err)
	}

	// The Transport rides the injected Network. traceDial forwards the
	// standard httptrace ConnectStart/ConnectDone hooks around the injected
	// dial: net.Dialer fires them itself, but a netpath.Network (memory
	// fabric, datapath-backed stack) has no obligation to, and the connect
	// timing must not silently vanish on non-kernel networks.
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	tr := &http.Transport{
		DialContext:           traceDial(o.Network.DialContext),
		TLSClientConfig:       tcfg,
		ForceAttemptHTTP2:     useH2, // explicit: custom DialContext+TLSClientConfig otherwise disable h2
		DisableCompression:    true,  // synthetic bodies are incompressible; keep sizes exact
		MaxIdleConns:          8,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	seed := o.Seed
	if seed == 0 {
		seed = time.Now().UnixNano() // unseeded runs must not all draw one sequence
	}
	return &client{
		hc:           &http.Client{Transport: tr},
		tr:           tr,
		baseURL:      scheme + "://" + o.Target,
		hostOverride: host,
		urlPath:      urlPath,
		objectSize:   sizeDist,
		think:        thinkDist,
		objects:      objects,
		seed:         seed,
		rec:          newRecorder(),
	}, nil
}

// traceDial wraps an injected DialContext so the request's httptrace
// ConnectStart/ConnectDone hooks fire around it, as they would with
// net.Dialer. When the underlying network itself dials through net.Dialer
// (netpath "host"), the hooks fire a second time from inside; the client's
// trace callbacks are first-fire-wins so both shapes yield one consistent
// ConnectMs definition.
func traceDial(dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		trace := httptrace.ContextClientTrace(ctx)
		if trace != nil && trace.ConnectStart != nil {
			trace.ConnectStart(network, addr)
		}
		c, err := dial(ctx, network, addr)
		if trace != nil && trace.ConnectDone != nil {
			trace.ConnectDone(network, addr, err)
		}
		return c, err
	}
}

// Name implements app.Client.
func (c *client) Name() string { return Name }

// Counters implements app.Client: one "packet" per completed request,
// response-body bytes (goodput accounting, headers excluded).
func (c *client) Counters() *accounting.Counters { return &c.counters }

// Metrics implements metrics.Source. Each call closes one observation
// interval (window percentiles/goodput, the core/app/voip discipline).
func (c *client) Metrics() metrics.Snapshot { return c.rec.Metrics() }

// CumulativeMetrics returns the whole-run snapshot without closing an
// observation interval — the final-sample capability the agent discovers by
// assertion.
func (c *client) CumulativeMetrics() metrics.Snapshot { return c.rec.Cumulative() }

// Close implements io.Closer for the built-but-never-run teardown path,
// releasing the transport's idle connections. Idempotent.
func (c *client) Close() error {
	c.tr.CloseIdleConnections()
	return nil
}

// Run implements app.Client: fetch objects until the count is reached or ctx
// ends (the flow's duration bound), sleeping a think-time draw between
// requests. Request failures are recorded in the metrics plane (Errors), not
// returned — a lossy path is a measurement, not a flow failure. Run may be
// called once; it returns nil on clean completion or cancellation.
func (c *client) Run(ctx context.Context) error {
	if !c.started.CompareAndSwap(false, true) {
		return errors.New("httpx: client already run")
	}
	c.rec.runStarted()
	defer c.rec.runStopped()
	defer c.tr.CloseIdleConnections()

	rng := rand.New(rand.NewSource(c.seed))
	for i := 0; c.objects == 0 || i < c.objects; i++ {
		if ctx.Err() != nil {
			return nil
		}
		path := c.urlPath
		if path == "" {
			path = fmt.Sprintf("/object/%d", uint64(c.objectSize.Sample(rng)))
		}
		err := c.do(ctx, path)
		if ctx.Err() != nil {
			return nil
		}
		pause := time.Duration(c.think.Sample(rng))
		if err != nil && pause < errPause {
			pause = errPause
		}
		if pause > 0 {
			t := time.NewTimer(pause)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return nil
			}
		}
	}
	return nil
}

// do issues one GET and records its sample: connect/TLS-handshake times from
// httptrace (new connections only), TTFB from request start to the first
// response byte, Object to the last body byte. A request interrupted by ctx
// cancellation is not recorded — a truncated transfer is shutdown, not a
// quality data point.
func (c *client) do(ctx context.Context, path string) error {
	// Trace state is mutex-guarded: most hooks land before Do returns
	// (synchronized through the response), but a transport dial race can
	// complete a background dial — firing ConnectDone/TLSHandshakeDone with
	// this request's trace — after Do has already returned on an idle conn.
	var (
		s                   sample
		mu                  sync.Mutex
		connStart, tlsStart time.Time
		reqStart            time.Time
		ttfb                time.Duration
	)
	trace := &httptrace.ClientTrace{
		// On the "host" network the hooks fire twice per dial: once from
		// traceDial and once from net.Dialer inside it (per address attempt,
		// under happy-eyeballs). The guards keep one definition of ConnectMs on
		// every network — first ConnectStart (dial initiation) to first
		// successful ConnectDone — instead of last-attempt-start to outer
		// return on kernel paths only.
		ConnectStart: func(_, _ string) {
			mu.Lock()
			if connStart.IsZero() {
				connStart = time.Now()
			}
			mu.Unlock()
		},
		ConnectDone: func(_, _ string, err error) {
			mu.Lock()
			if err == nil && !connStart.IsZero() && s.Connect == 0 {
				s.Connect = time.Since(connStart)
			}
			mu.Unlock()
		},
		TLSHandshakeStart: func() {
			mu.Lock()
			tlsStart = time.Now()
			mu.Unlock()
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			mu.Lock()
			if err == nil && !tlsStart.IsZero() {
				s.TLSHandshake = time.Since(tlsStart)
			}
			mu.Unlock()
		},
		GotFirstResponseByte: func() {
			mu.Lock()
			ttfb = time.Since(reqStart)
			mu.Unlock()
		},
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		s.Err = true
		c.rec.observe(s)
		return err
	}
	if c.hostOverride != "" {
		req.Host = c.hostOverride
	}
	mu.Lock()
	reqStart = time.Now()
	mu.Unlock()
	resp, err := c.hc.Do(req)
	// snap copies the hook-written fields out under the lock; late background
	// hooks then mutate only their own s, never the recorded copy.
	snap := func() sample {
		mu.Lock()
		defer mu.Unlock()
		out := s
		out.TTFB = ttfb
		return out
	}
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		out := snap()
		out.Err = true
		out.Object = time.Since(reqStart)
		if out.TTFB == 0 {
			out.TTFB = out.Object
		}
		c.rec.observe(out)
		return err
	}
	n, cerr := io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if cerr != nil && ctx.Err() != nil {
		return cerr
	}
	out := snap()
	out.Object = time.Since(reqStart)
	if out.TTFB == 0 {
		out.TTFB = out.Object
	}
	out.Bytes = uint64(n)
	out.Proto = resp.Proto
	if cerr != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		out.Err = true
	}
	c.counters.Add(uint64(n))
	c.rec.observe(out)
	if cerr != nil {
		return cerr
	}
	if out.Err {
		return fmt.Errorf("httpx: GET %s: status %s", path, resp.Status)
	}
	return nil
}
