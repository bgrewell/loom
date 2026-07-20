// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/app"
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

// buildServer builds an origin through the app registry (the agent's path).
func buildServer(t *testing.T, n netpath.Network, params map[string]string) app.Server {
	t.Helper()
	srv, err := app.ServerRegistry.Build(Name, app.Options{Params: params, Network: n, Seed: 11})
	if err != nil {
		t.Fatalf("Build server: %v", err)
	}
	t.Cleanup(func() { _ = srv.(io.Closer).Close() })
	if srv.Name() != Name {
		t.Fatalf("server Name = %q, want %q", srv.Name(), Name)
	}
	if srv.Addr().Port() == 0 {
		t.Fatal("server Addr not valid at build time")
	}
	return srv
}

// buildClient builds a client through the app registry, aimed at srv over n.
// The URL host is "localhost": the memory fabric routes by port, and
// "localhost" is among the generated cert's SANs, so TLS tests exercise real
// certificate verification.
func buildClient(t *testing.T, n netpath.Network, srv app.Server, params map[string]string) app.Client {
	t.Helper()
	target := fmt.Sprintf("localhost:%d", srv.Addr().Port())
	cli, err := app.ClientRegistry.Build(Name, app.Options{Params: params, Network: n, Target: target, Seed: 13})
	if err != nil {
		t.Fatalf("Build client: %v", err)
	}
	t.Cleanup(func() { _ = cli.(io.Closer).Close() })
	return cli
}

// pinnedParams returns client params with the origin's published cert pinned
// as tls_ca — the secure-by-default path every TLS round trip uses.
func pinnedParams(t *testing.T, srv app.Server, extra map[string]string) map[string]string {
	t.Helper()
	pem := srv.(interface{ CertificatePEM() []byte }).CertificatePEM()
	if len(pem) == 0 {
		t.Fatal("origin published no certificate PEM")
	}
	m := map[string]string{"tls": "true", "tls_ca": base64.StdEncoding.EncodeToString(pem)}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// runPair runs both sides, waits for the client to finish its bounded object
// count, cancels the server, and returns the client's cumulative snapshot.
func runPair(t *testing.T, srv app.Server, cli app.Client) metrics.HTTP {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Run(ctx) }()
	if err := cli.Run(ctx); err != nil {
		t.Fatalf("client Run: %v", err)
	}
	cum, ok := cli.(interface{ CumulativeMetrics() metrics.Snapshot }).CumulativeMetrics().(metrics.HTTP)
	if !ok {
		t.Fatal("client CumulativeMetrics is not metrics.HTTP")
	}
	cancel()
	if err := <-srvDone; err != nil {
		t.Fatalf("server Run: %v", err)
	}
	return cum
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

// protos returns the distinct response protocol strings the client recorded.
func protos(cli app.Client) map[string]int {
	c := cli.(*client)
	c.rec.mu.Lock()
	defer c.rec.mu.Unlock()
	m := map[string]int{}
	for _, s := range c.rec.samples {
		m[s.Proto]++
	}
	return m
}

// requestSizes returns the per-request body sizes the client recorded.
func requestSizes(cli app.Client) []uint64 {
	c := cli.(*client)
	c.rec.mu.Lock()
	defer c.rec.mu.Unlock()
	var out []uint64
	for _, s := range c.rec.samples {
		out = append(out, s.Bytes)
	}
	return out
}

// TestH1RoundTrip: plain HTTP/1.1 over the memory fabric, built through the
// registries; timings, counters, and both sides' snapshots populate.
func TestH1RoundTrip(t *testing.T) {
	cn, sn := memPair(t)
	srv := buildServer(t, sn, nil)
	cli := buildClient(t, cn, srv, map[string]string{"url_path": "/object/65536", "objects": "4"})
	h := runPair(t, srv, cli)

	if h.Requests != 4 || h.Errors != 0 {
		t.Fatalf("requests/errors = %d/%d, want 4/0", h.Requests, h.Errors)
	}
	if h.TTFBMsP50 <= 0 || h.TTFBMsP95 < h.TTFBMsP50 || h.TTFBMsP99 < h.TTFBMsP95 {
		t.Errorf("TTFB percentiles not populated/ordered: p50=%v p95=%v p99=%v", h.TTFBMsP50, h.TTFBMsP95, h.TTFBMsP99)
	}
	if h.ObjectMsP50 <= 0 || h.ObjectMsP95 < h.ObjectMsP50 || h.ObjectMsP99 < h.ObjectMsP95 {
		t.Errorf("object percentiles not populated/ordered: p50=%v p95=%v p99=%v", h.ObjectMsP50, h.ObjectMsP95, h.ObjectMsP99)
	}
	if h.ConnectMs <= 0 {
		t.Errorf("ConnectMs = %v, want > 0 (dial timing must survive the injected network)", h.ConnectMs)
	}
	if h.TLSHandshakeMs != 0 {
		t.Errorf("TLSHandshakeMs = %v on a plaintext flow, want 0", h.TLSHandshakeMs)
	}
	if h.GoodputMbps <= 0 {
		t.Errorf("GoodputMbps = %v, want > 0", h.GoodputMbps)
	}
	if got := cli.Counters().Bytes(); got != 4*65536 {
		t.Errorf("client counters bytes = %d, want %d", got, 4*65536)
	}
	if got := cli.Counters().Packets(); got != 4 {
		t.Errorf("client counters packets = %d, want 4 (one per request)", got)
	}
	// The origin accounts a response after its handler returns, which can
	// trail the client's final body read by a scheduler beat — wait, don't
	// assert instantaneously.
	waitFor(t, 5*time.Second, "server accounting to catch up", func() bool {
		return srv.Counters().Bytes() == 4*65536 && srv.Counters().Packets() == 4
	})
	sh, ok := srv.(metrics.Source).Metrics().(metrics.HTTP)
	if !ok || sh.Requests != 4 || sh.Errors != 0 {
		t.Errorf("server snapshot = %+v (ok=%v), want 4 requests, 0 errors", sh, ok)
	}
	if pr := protos(cli); pr["HTTP/1.1"] != 4 {
		t.Errorf("protocols = %v, want 4×HTTP/1.1", pr)
	}
	if kind := cli.(metrics.Source).Metrics().Kind(); kind != metrics.KindHTTP {
		t.Errorf("snapshot Kind = %q, want %q", kind, metrics.KindHTTP)
	}
}

// TestH1TLSRoundTrip: HTTPS with the origin's self-signed cert pinned via
// tls_ca — certificate verification stays ON — negotiating HTTP/1.1.
func TestH1TLSRoundTrip(t *testing.T) {
	cn, sn := memPair(t)
	srv := buildServer(t, sn, map[string]string{"tls": "true"})
	cli := buildClient(t, cn, srv, pinnedParams(t, srv, map[string]string{"url_path": "/object/4096", "objects": "3"}))
	h := runPair(t, srv, cli)

	if h.Requests != 3 || h.Errors != 0 {
		t.Fatalf("requests/errors = %d/%d, want 3/0", h.Requests, h.Errors)
	}
	if h.TLSHandshakeMs <= 0 {
		t.Errorf("TLSHandshakeMs = %v, want > 0 on TLS", h.TLSHandshakeMs)
	}
	if h.ConnectMs <= 0 || h.TTFBMsP50 <= 0 {
		t.Errorf("connect/TTFB not populated: connect=%v ttfb=%v", h.ConnectMs, h.TTFBMsP50)
	}
	if pr := protos(cli); pr["HTTP/1.1"] != 3 {
		t.Errorf("protocols = %v, want 3×HTTP/1.1 (h2 not requested)", pr)
	}
}

// TestH2TLSRoundTrip: HTTPS + ALPN h2 end to end over the injected network,
// still under full certificate verification.
func TestH2TLSRoundTrip(t *testing.T) {
	cn, sn := memPair(t)
	srv := buildServer(t, sn, map[string]string{"tls": "true", "h2": "true"})
	cli := buildClient(t, cn, srv, pinnedParams(t, srv, map[string]string{"url_path": "/object/8192", "objects": "3", "h2": "true"}))
	h := runPair(t, srv, cli)

	if h.Requests != 3 || h.Errors != 0 {
		t.Fatalf("requests/errors = %d/%d, want 3/0", h.Requests, h.Errors)
	}
	if h.TLSHandshakeMs <= 0 {
		t.Errorf("TLSHandshakeMs = %v, want > 0 on TLS", h.TLSHandshakeMs)
	}
	if pr := protos(cli); pr["HTTP/2.0"] != 3 {
		t.Errorf("protocols = %v, want 3×HTTP/2.0", pr)
	}
}

// TestH2ServerRefusesWithoutClientH2: an h2-enabled origin still serves a
// plain HTTP/1.1-only TLS client (ALPN downgrade, not failure).
func TestH2ServerServesH1Client(t *testing.T) {
	cn, sn := memPair(t)
	srv := buildServer(t, sn, map[string]string{"tls": "true", "h2": "true"})
	cli := buildClient(t, cn, srv, pinnedParams(t, srv, map[string]string{"url_path": "/object/512", "objects": "2"}))
	h := runPair(t, srv, cli)
	if h.Requests != 2 || h.Errors != 0 {
		t.Fatalf("requests/errors = %d/%d, want 2/0", h.Requests, h.Errors)
	}
	if pr := protos(cli); pr["HTTP/1.1"] != 2 {
		t.Errorf("protocols = %v, want 2×HTTP/1.1", pr)
	}
}

// TestHostOverrideSNI: with a target whose host part matches nothing in the
// cert, the host param supplies both SNI/verification name and the Host
// header, so pinned verification still succeeds.
func TestHostOverrideSNI(t *testing.T) {
	cn, sn := memPair(t)
	srv := buildServer(t, sn, map[string]string{"tls": "true"})
	params := pinnedParams(t, srv, map[string]string{"url_path": "/object/1024", "objects": "2", "host": "localhost"})
	// Deliberately not "localhost": the memory fabric routes by port alone,
	// and the cert has no SAN for this name — only the host param makes
	// verification pass.
	target := fmt.Sprintf("origin.invalid:%d", srv.Addr().Port())
	cli, err := app.ClientRegistry.Build(Name, app.Options{Params: params, Network: cn, Target: target, Seed: 13})
	if err != nil {
		t.Fatalf("Build client: %v", err)
	}
	t.Cleanup(func() { _ = cli.(io.Closer).Close() })
	h := runPair(t, srv, cli)
	if h.Requests != 2 || h.Errors != 0 {
		t.Fatalf("requests/errors = %d/%d, want 2/0", h.Requests, h.Errors)
	}
}

// TestSecureByDefault: without tls_ca the self-signed origin is REJECTED —
// verification is on by default and failures land in Errors, not silently
// succeed. tls_insecure is the explicit opt-out.
func TestSecureByDefault(t *testing.T) {
	cn, sn := memPair(t)
	srv := buildServer(t, sn, map[string]string{"tls": "true"})
	// The memory fabric's streams are unbuffered net.Pipes, so a rejected
	// handshake write-write deadlocks (client blocked writing its alert,
	// server blocked writing the tail of its flight) until the server's
	// ReadHeaderTimeout rescues both. Real TCP has socket buffers; shorten
	// the rescue so the test doesn't idle for the production timeout.
	srv.(*origin).srv.ReadHeaderTimeout = 250 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Run(ctx) }()

	unpinned := buildClient(t, cn, srv, map[string]string{"tls": "true", "url_path": "/object/64", "objects": "1"})
	if err := unpinned.Run(ctx); err != nil {
		t.Fatalf("client Run: %v", err)
	}
	h := unpinned.(interface{ CumulativeMetrics() metrics.Snapshot }).CumulativeMetrics().(metrics.HTTP)
	if h.Requests != 1 || h.Errors != 1 {
		t.Fatalf("unpinned client: requests/errors = %d/%d, want 1/1 (x509 rejection)", h.Requests, h.Errors)
	}
	if got := unpinned.Counters().Bytes(); got != 0 {
		t.Errorf("unpinned client fetched %d bytes through an unverified origin", got)
	}

	insecure := buildClient(t, cn, srv, map[string]string{"tls": "true", "tls_insecure": "true", "url_path": "/object/64", "objects": "1"})
	if err := insecure.Run(ctx); err != nil {
		t.Fatalf("insecure client Run: %v", err)
	}
	h = insecure.(interface{ CumulativeMetrics() metrics.Snapshot }).CumulativeMetrics().(metrics.HTTP)
	if h.Requests != 1 || h.Errors != 0 {
		t.Fatalf("tls_insecure client: requests/errors = %d/%d, want 1/0", h.Requests, h.Errors)
	}

	cancel()
	if err := <-srvDone; err != nil {
		t.Fatalf("server Run: %v", err)
	}
}

// TestObjectExactSizesAndDeterminism: /object/{bytes} returns exactly the
// requested byte count, with identical bodies across fetches (seeded
// deterministic content), including the 0-byte edge.
func TestObjectExactSizesAndDeterminism(t *testing.T) {
	cn, sn := memPair(t)
	srv := buildServer(t, sn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Run(ctx) }()

	hc := &http.Client{Transport: &http.Transport{DialContext: cn.DialContext}}
	base := fmt.Sprintf("http://localhost:%d", srv.Addr().Port())

	var firstSum [32]byte
	for i, size := range []int64{0, 1, 3, 1000, 65536, 1<<20 + 7} {
		body, resp := get(t, hc, fmt.Sprintf("%s/object/%d", base, size))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /object/%d: status %s", size, resp.Status)
		}
		if resp.ContentLength != size {
			t.Errorf("GET /object/%d: Content-Length %d", size, resp.ContentLength)
		}
		if int64(len(body)) != size {
			t.Errorf("GET /object/%d: body %d bytes", size, len(body))
		}
		if i == 0 {
			firstSum = sha256.Sum256(body)
			again, _ := get(t, hc, fmt.Sprintf("%s/object/%d", base, size))
			if sha256.Sum256(again) != firstSum {
				t.Error("same path served different bytes across fetches")
			}
		}
	}
	// Deterministic across fetches for a non-trivial size too.
	b1, _ := get(t, hc, base+"/object/100000")
	b2, _ := get(t, hc, base+"/object/100000")
	if sha256.Sum256(b1) != sha256.Sum256(b2) {
		t.Error("100000-byte object not deterministic across fetches")
	}
	// Out-of-range and malformed sizes are refused.
	for _, p := range []string{"/object/-1", "/object/enormous", fmt.Sprintf("/object/%d", int64(maxObjectBytes)+1)} {
		if _, resp := get(t, hc, base+p); resp.StatusCode == http.StatusOK {
			t.Errorf("GET %s succeeded, want rejection", p)
		}
	}

	cancel()
	if err := <-srvDone; err != nil {
		t.Fatalf("server Run: %v", err)
	}
}

// get fetches url, returning the whole body and the response.
func get(t *testing.T, hc *http.Client, url string) ([]byte, *http.Response) {
	t.Helper()
	resp, err := hc.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: read body: %v", url, err)
	}
	return body, resp
}
