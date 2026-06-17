// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"time"

	"github.com/bgrewell/loom/core/scenario"
)

// clientOpts captures the resolved client-side test parameters used to synthesize
// a scenario (the iperf-style CLI's equivalent of a scenario YAML).
type clientOpts struct {
	host       string // server host (data + control target)
	clientIP   string // client's routable source IP (so the server can target it in reverse/bidir)
	udp        bool   // UDP instead of TCP
	packetSize int
	rate       string // e.g. "1G", "100Mbps"; empty = unlimited
	duration   time.Duration
	volume     uint64 // stop after N bytes (0 = time-bounded)
	parallel   int
	reverse    bool
	bidir      bool
}

// endpoint names used in the synthesized scenario.
const (
	epClient = "client"
	epServer = "server"
)

// buildScenario turns resolved client options into a scenario the controller can
// run: two endpoints (this client + the remote server) and one event per stream
// per direction. TCP/UDP is selected via the event datapath; packet size and rate
// ride the flow params, exactly as a YAML scenario would. The flow kind is
// "stream" (a non-emulation), so the standard sender/receiver path runs.
func buildScenario(o clientOpts) *scenario.Scenario {
	dp := "tcp"
	if o.udp {
		dp = "udp"
	}

	stop := scenario.Stop{}
	if o.volume > 0 {
		stop.Volume = o.volume
	} else {
		stop.After = o.duration
	}

	params := func() map[string]any {
		p := map[string]any{"packet_size": o.packetSize}
		if o.rate != "" {
			p["rate"] = o.rate
		}
		return p
	}

	mk := func(name, from, to string) scenario.Event {
		return scenario.Event{
			Name:     name,
			Flow:     scenario.Flow{Kind: "stream", Params: params()},
			From:     scenario.Selector{Raw: from},
			To:       scenario.Selector{Raw: to},
			Datapath: dp,
			Start:    scenario.Start{Offset: 0},
			Stop:     stop,
		}
	}

	p := o.parallel
	if p < 1 {
		p = 1
	}
	var events []scenario.Event
	for i := 0; i < p; i++ {
		switch {
		case o.bidir:
			events = append(events,
				mk(fmt.Sprintf("up-%d", i), epClient, epServer),
				mk(fmt.Sprintf("down-%d", i), epServer, epClient))
		case o.reverse:
			events = append(events, mk(fmt.Sprintf("stream-%d", i), epServer, epClient))
		default:
			events = append(events, mk(fmt.Sprintf("stream-%d", i), epClient, epServer))
		}
	}

	return &scenario.Scenario{
		Name: "loom",
		Seed: 1,
		Endpoints: []scenario.Endpoint{
			{Name: epClient, Address: o.clientIP},
			{Name: epServer, Address: o.host},
		},
		Timeline: events,
	}
}
