// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/app"
	"github.com/bgrewell/loom/core/metrics"
	"github.com/bgrewell/loom/core/netpath"
)

// TestObjectSizeDistRespected: with object_size "1KB..2KB" every generated
// request stays inside the range and the draws actually vary (statistical,
// loose — seeded, so stable in CI).
func TestObjectSizeDistRespected(t *testing.T) {
	cn, sn := memPair(t)
	srv := buildServer(t, sn, nil)
	cli := buildClient(t, cn, srv, map[string]string{"object_size": "1KB..2KB", "objects": "40"})
	h := runPair(t, srv, cli)
	if h.Requests != 40 || h.Errors != 0 {
		t.Fatalf("requests/errors = %d/%d, want 40/0", h.Requests, h.Errors)
	}
	sizes := requestSizes(cli)
	lo, hi := sizes[0], sizes[0]
	for _, s := range sizes {
		if s < 1000 || s > 2000 {
			t.Fatalf("request size %d outside 1000..2000", s)
		}
		if s < lo {
			lo = s
		}
		if s > hi {
			hi = s
		}
	}
	if lo == hi {
		t.Errorf("all 40 draws identical (%d bytes) — distribution not sampled", lo)
	}
	// Loose spread check: a uniform 1000..2000 draw of 40 should span more
	// than a tenth of the range.
	if hi-lo < 100 {
		t.Errorf("draws span only %d bytes over 40 requests", hi-lo)
	}
}

// TestThinkTimeRespected: with think "20ms" and 5 objects the run cannot
// complete faster than the 4 inter-request pauses (loose lower bound only —
// upper bounds flake in CI).
func TestThinkTimeRespected(t *testing.T) {
	cn, sn := memPair(t)
	srv := buildServer(t, sn, nil)
	cli := buildClient(t, cn, srv, map[string]string{"url_path": "/object/128", "objects": "5", "think": "20ms"})
	start := time.Now()
	runPair(t, srv, cli)
	if elapsed := time.Since(start); elapsed < 4*20*time.Millisecond {
		t.Errorf("5 requests with 20ms think finished in %v, want >= 80ms", elapsed)
	}
}

// TestClientUnboundedStopsOnCtx: objects=0 runs until the context (flow
// duration) ends, then Run returns nil.
func TestClientUnboundedStopsOnCtx(t *testing.T) {
	cn, sn := memPair(t)
	srv := buildServer(t, sn, nil)
	cli := buildClient(t, cn, srv, map[string]string{"url_path": "/object/256"})

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Run(ctx) }()
	if err := cli.Run(ctx); err != nil {
		t.Fatalf("client Run: %v", err)
	}
	h := cli.(interface{ CumulativeMetrics() metrics.Snapshot }).CumulativeMetrics().(metrics.HTTP)
	if h.Requests == 0 {
		t.Error("unbounded client completed no requests before the deadline")
	}
	if err := <-srvDone; err != nil {
		t.Fatalf("server Run: %v", err)
	}
}

// TestMetricsWindows: Metrics closes an observation interval (second call
// covers only new requests), CumulativeMetrics never does.
func TestMetricsWindows(t *testing.T) {
	cn, sn := memPair(t)
	srv := buildServer(t, sn, nil)
	cli := buildClient(t, cn, srv, map[string]string{"url_path": "/object/1024", "objects": "6"})
	runPair(t, srv, cli)

	first, ok := cli.(metrics.Source).Metrics().(metrics.HTTP)
	if !ok || first.Requests != 6 {
		t.Fatalf("first window = %+v, want 6 requests", first)
	}
	second := cli.(metrics.Source).Metrics().(metrics.HTTP)
	if second.Requests != 0 {
		t.Errorf("second window saw %d requests, want 0 (interval closed)", second.Requests)
	}
	cum := cli.(interface{ CumulativeMetrics() metrics.Snapshot }).CumulativeMetrics().(metrics.HTTP)
	if cum.Requests != 6 {
		t.Errorf("cumulative = %d requests, want 6 (windows must not consume it)", cum.Requests)
	}
}

// TestFactoryErrors pins Build-time validation: every bad knob is an error at
// Build, not a surprise mid-flow.
func TestFactoryErrors(t *testing.T) {
	_, nb := netpath.Memory()
	defer nb.Close()
	tgt := "localhost:80"
	cases := []struct {
		name   string
		client bool
		opts   app.Options
	}{
		{"client nil network", true, app.Options{Target: tgt}},
		{"client missing target", true, app.Options{Network: nb}},
		{"h2 without tls", true, app.Options{Network: nb, Target: tgt, Params: map[string]string{"h2": "true"}}},
		{"negative objects", true, app.Options{Network: nb, Target: tgt, Params: map[string]string{"objects": "-1"}}},
		{"malformed objects", true, app.Options{Network: nb, Target: tgt, Params: map[string]string{"objects": "many"}}},
		{"bad object_size dist", true, app.Options{Network: nb, Target: tgt, Params: map[string]string{"object_size": "big..small"}}},
		{"bad think dist", true, app.Options{Network: nb, Target: tgt, Params: map[string]string{"think": "whenever"}}},
		{"relative url_path", true, app.Options{Network: nb, Target: tgt, Params: map[string]string{"url_path": "object/1"}}},
		{"tls_ca without tls", true, app.Options{Network: nb, Target: tgt, Params: map[string]string{"tls_ca": "aGk="}}},
		{"tls_ca bad base64", true, app.Options{Network: nb, Target: tgt, Params: map[string]string{"tls": "true", "tls_ca": "not base64!!"}}},
		{"tls_ca no certs", true, app.Options{Network: nb, Target: tgt, Params: map[string]string{"tls": "true", "tls_ca": base64.StdEncoding.EncodeToString([]byte("hello"))}}},
		{"tls_insecure malformed", true, app.Options{Network: nb, Target: tgt, Params: map[string]string{"tls": "true", "tls_insecure": "maybe"}}},
		{"server nil network", false, app.Options{}},
		{"server h2 without tls", false, app.Options{Network: nb, Params: map[string]string{"h2": "true"}}},
		{"server bad ladder", false, app.Options{Network: nb, Params: map[string]string{"ladder": "240p400k"}}},
		{"server zero-rate rung", false, app.Options{Network: nb, Params: map[string]string{"ladder": "240p:100"}}},
		{"server duplicate rung", false, app.Options{Network: nb, Params: map[string]string{"ladder": "a:400k,b:400k"}}},
		{"server bad seg_duration", false, app.Options{Network: nb, Params: map[string]string{"seg_duration": "-4s"}}},
		{"server zero segments", false, app.Options{Network: nb, Params: map[string]string{"segments": "0"}}},
		{"server inverted port range", false, app.Options{Network: nb, Params: map[string]string{"port_min": "5006", "port_max": "5004"}}},
		{"server port_max without port_min", false, app.Options{Network: nb, Params: map[string]string{"port_max": "40100"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.client {
				_, err = NewClient(tc.opts)
			} else {
				var srv app.Server
				srv, err = NewServer(tc.opts)
				if err == nil {
					_ = srv.(*origin).Close()
				}
			}
			if err == nil {
				t.Fatal("Build succeeded, want error")
			}
		})
	}
}
