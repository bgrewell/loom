// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package voip

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/app"
	"github.com/bgrewell/loom/core/metrics"
	"github.com/bgrewell/loom/core/netpath"
)

// TestRegisteredCall drives a call built entirely through the app registries
// — the agent's path: server bound inside port_min..port_max with Addr
// advertised at build time, client dialed at that address, both exposing
// metrics.Source snapshots and live counters.
func TestRegisteredCall(t *testing.T) {
	na, nb := netpath.Memory()
	defer na.Close()
	defer nb.Close()

	srvAny, err := app.ServerRegistry.Build("voip", app.Options{
		Params:  map[string]string{"port_min": "45000", "port_max": "45010", "codec": "pcmu", "jb_ms": "80"},
		Network: nb,
		Seed:    7,
	})
	if err != nil {
		t.Fatalf("Build server: %v", err)
	}
	if srvAny.Name() != "voip" {
		t.Errorf("server Name = %q, want voip", srvAny.Name())
	}
	port := srvAny.Addr().Port()
	if port < 45000 || port > 45010 {
		t.Fatalf("server bound port %d outside 45000..45010", port)
	}

	clAny, err := app.ClientRegistry.Build("voip", app.Options{
		Params:  map[string]string{"codec": "pcmu", "jb_ms": "80", "handshake_timeout_ms": "4000"},
		Network: na,
		Target:  "127.0.0.1:45000",
		Seed:    9,
		OWD:     zeroOffset{},
	})
	if err != nil {
		t.Fatalf("Build client: %v", err)
	}

	// Shorten the RTCP cadence for CI (in-package access; the wire default
	// is the RFC's 5 s minimum).
	srvAny.(*server).sess.setRTCPTmin(testRTCPTmin)
	clAny.(*client).sess.setRTCPTmin(testRTCPTmin)

	ctx, cancel := context.WithCancel(context.Background())
	srvDone := make(chan error, 1)
	clDone := make(chan error, 1)
	go func() { srvDone <- srvAny.Run(ctx) }()
	go func() { clDone <- clAny.Run(ctx) }()

	// Both sides are metrics.Sources with voip-kind snapshots.
	src, ok := clAny.(metrics.Source)
	if !ok {
		t.Fatal("client does not implement metrics.Source")
	}
	if _, ok := srvAny.(metrics.Source); !ok {
		t.Fatal("server does not implement metrics.Source")
	}
	waitFor(t, 10*time.Second, "media in both directions", func() bool {
		v, ok := src.Metrics().(metrics.VoIP)
		return ok && v.RxPackets > 10 && v.TxPackets > 10
	})
	if kind := src.Metrics().Kind(); kind != metrics.KindVoIP {
		t.Errorf("snapshot Kind = %q, want %q", kind, metrics.KindVoIP)
	}
	if clAny.Counters().Packets() == 0 || srvAny.Counters().Packets() == 0 {
		t.Error("accounting counters not wired")
	}

	cancel()
	if err := <-clDone; err != nil {
		t.Errorf("client Run: %v", err)
	}
	if err := <-srvDone; err != nil {
		t.Errorf("server Run: %v", err)
	}
}

// TestFactoryErrors pins the factories' parameter validation: every bad knob
// is reported at Build time, not discovered mid-flow.
func TestFactoryErrors(t *testing.T) {
	_, nb := netpath.Memory()
	defer nb.Close()
	cases := []struct {
		name   string
		client bool
		opts   app.Options
	}{
		{"nil network", true, app.Options{Target: "127.0.0.1:5004"}},
		{"missing target", true, app.Options{Network: nb}},
		{"bad target port", true, app.Options{Network: nb, Target: "127.0.0.1:notaport"}},
		{"unknown codec", true, app.Options{Network: nb, Target: "127.0.0.1:5004", Params: map[string]string{"codec": "g711000"}}},
		{"bad direction", true, app.Options{Network: nb, Target: "127.0.0.1:5004", Params: map[string]string{"direction": "sideways"}}},
		{"malformed jb_ms", true, app.Options{Network: nb, Target: "127.0.0.1:5004", Params: map[string]string{"jb_ms": "forty"}}},
		{"inverted port range", false, app.Options{Network: nb, Params: map[string]string{"port_min": "5006", "port_max": "5004"}}},
		{"port_max without port_min", false, app.Options{Network: nb, Params: map[string]string{"port_max": "40100"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.client {
				_, err = NewClient(tc.opts)
			} else {
				_, err = NewServer(tc.opts)
			}
			if err == nil {
				t.Fatal("Build succeeded, want error")
			}
		})
	}
}

// TestPortRangeExhaustion: when every port in the range is taken, the server
// factory reports it instead of binding elsewhere (firewall determinism).
func TestPortRangeExhaustion(t *testing.T) {
	_, nb := netpath.Memory()
	defer nb.Close()
	first, err := NewServer(app.Options{Network: nb, Params: map[string]string{"port_min": "46000", "port_max": "46000"}})
	if err != nil {
		t.Fatalf("first server: %v", err)
	}
	if p := first.Addr().Port(); p != 46000 {
		t.Fatalf("first server bound %d, want 46000", p)
	}
	if _, err := NewServer(app.Options{Network: nb, Params: map[string]string{"port_min": "46000", "port_max": "46000"}}); err == nil {
		t.Fatal("second server bound an exhausted range")
	}
	// A built-but-never-run server must be closable (io.Closer assertion —
	// the Configure-then-Destroy-without-Start path), releasing its port for
	// the next bind.
	if err := first.(io.Closer).Close(); err != nil {
		t.Fatalf("Close(first): %v", err)
	}
	third, err := NewServer(app.Options{Network: nb, Params: map[string]string{"port_min": "46000", "port_max": "46000"}})
	if err != nil {
		t.Fatalf("rebind after Close: %v", err)
	}
	if p := third.Addr().Port(); p != 46000 {
		t.Fatalf("rebound server on %d, want 46000", p)
	}
}
