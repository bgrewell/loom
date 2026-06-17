// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
	"time"
)

func TestBuildScenario(t *testing.T) {
	base := clientOpts{host: "10.0.0.2", clientIP: "10.0.0.1", packetSize: 1400, duration: 5 * time.Second, parallel: 1}

	t.Run("forward defaults to one tcp event client->server", func(t *testing.T) {
		sc := buildScenario(base)
		if len(sc.Timeline) != 1 {
			t.Fatalf("events = %d, want 1", len(sc.Timeline))
		}
		e := sc.Timeline[0]
		if e.From.Raw != epClient || e.To.Raw != epServer {
			t.Errorf("direction = %s->%s, want client->server", e.From.Raw, e.To.Raw)
		}
		if e.Datapath != "tcp" || e.Flow.Kind != "stream" {
			t.Errorf("datapath/kind = %q/%q, want tcp/stream", e.Datapath, e.Flow.Kind)
		}
		if e.Stop.After != 5*time.Second || e.Stop.Volume != 0 {
			t.Errorf("stop = %+v, want After=5s", e.Stop)
		}
		if e.Flow.Params["packet_size"] != 1400 {
			t.Errorf("packet_size = %v, want 1400", e.Flow.Params["packet_size"])
		}
		// Endpoint addresses carry through so reverse/bidir can target the client.
		got := map[string]string{}
		for _, ep := range sc.Endpoints {
			got[ep.Name] = ep.Address
		}
		if got[epClient] != "10.0.0.1" || got[epServer] != "10.0.0.2" {
			t.Errorf("endpoint addrs = %v", got)
		}
	})

	t.Run("udp sets datapath and rate rides params", func(t *testing.T) {
		o := base
		o.udp = true
		o.rate = "1G"
		e := buildScenario(o).Timeline[0]
		if e.Datapath != "udp" {
			t.Errorf("datapath = %q, want udp", e.Datapath)
		}
		if e.Flow.Params["rate"] != "1G" {
			t.Errorf("rate = %v, want 1G", e.Flow.Params["rate"])
		}
	})

	t.Run("reverse flips direction", func(t *testing.T) {
		o := base
		o.reverse = true
		e := buildScenario(o).Timeline[0]
		if e.From.Raw != epServer || e.To.Raw != epClient {
			t.Errorf("direction = %s->%s, want server->client", e.From.Raw, e.To.Raw)
		}
	})

	t.Run("parallel produces N events", func(t *testing.T) {
		o := base
		o.parallel = 4
		if n := len(buildScenario(o).Timeline); n != 4 {
			t.Errorf("events = %d, want 4", n)
		}
	})

	t.Run("bidir produces 2N events in both directions", func(t *testing.T) {
		o := base
		o.bidir = true
		o.parallel = 2
		evs := buildScenario(o).Timeline
		if len(evs) != 4 {
			t.Fatalf("events = %d, want 4", len(evs))
		}
		fwd, rev := 0, 0
		for _, e := range evs {
			if e.From.Raw == epClient && e.To.Raw == epServer {
				fwd++
			}
			if e.From.Raw == epServer && e.To.Raw == epClient {
				rev++
			}
		}
		if fwd != 2 || rev != 2 {
			t.Errorf("fwd/rev = %d/%d, want 2/2", fwd, rev)
		}
	})

	t.Run("bytes overrides duration", func(t *testing.T) {
		o := base
		o.volume = 200 << 20
		e := buildScenario(o).Timeline[0]
		if e.Stop.Volume != 200<<20 || e.Stop.After != 0 {
			t.Errorf("stop = %+v, want Volume only", e.Stop)
		}
	})
}
