// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package flow

import (
	"fmt"
	"strings"
	"time"

	"github.com/bgrewell/loom/core/components"
	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/generator"
	"github.com/bgrewell/loom/core/scheduler"
	"github.com/bgrewell/loom/core/units"
)

// Spec is a transport-neutral description of a flow to build — shared by the CLI
// (`loom run`) and the agent (control plane). Empty fields take sensible
// defaults.
type Spec struct {
	Generator  string
	Payload    string
	Datapath   string
	Target     string // host:port for udp/tcp
	Iface      string // NIC name for afxdp
	Queue      int    // NIC queue for afxdp
	PacketSize int
	Rate       string // e.g. "100Mbps"; empty = unlimited
	Duration   time.Duration
	Count      uint64
	Volume     uint64
}

// Build constructs a Flow from a Spec, resolving the generator, scheduler, and
// datapath through c's registries (ADR-0022). A nil c uses components.Default().
// The caller owns the datapath and should Close it (via Flow.Datapath) when done.
func Build(spec Spec, c *components.Components) (*Flow, error) {
	c = components.OrDefault(c)

	gname := spec.Generator
	if gname == "" {
		gname = "stream"
	}
	gen, err := c.Generators.Build(gname, generator.Options{
		Payload:    spec.Payload,
		PacketSize: spec.PacketSize,
	})
	if err != nil {
		return nil, err
	}

	sched, err := schedulerForRate(c, spec.Rate, spec.PacketSize)
	if err != nil {
		return nil, err
	}

	dname := spec.Datapath
	if dname == "" {
		dname = "discard"
	}
	dp, err := c.TxDatapaths.Build(dname, datapath.Options{
		Addr: spec.Target, FrameSize: spec.PacketSize, Iface: spec.Iface, Queue: spec.Queue,
	})
	if err != nil {
		return nil, err
	}

	return &Flow{
		Generator: gen,
		Scheduler: sched,
		Datapath:  dp,
		MTU:       spec.PacketSize,
		Stop:      Stop{After: spec.Duration, Count: spec.Count, Volume: spec.Volume},
	}, nil
}

// schedulerForRate returns a soak scheduler for an empty rate, or an interval
// scheduler paced to approximate the given bit rate.
func schedulerForRate(c *components.Components, rate string, pkt int) (scheduler.Scheduler, error) {
	if strings.TrimSpace(rate) == "" {
		return scheduler.Soak{}, nil
	}
	bits, err := units.ParseRate(rate)
	if err != nil {
		return nil, err
	}
	if pkt < 1 {
		pkt = 1
	}
	pps := float64(bits) / float64(pkt*8)
	if pps <= 0 {
		return nil, fmt.Errorf("rate %q too low for packet size %d", rate, pkt)
	}
	gap := time.Duration(float64(time.Second) / pps)
	if gap < 1 {
		gap = 1
	}
	return c.Schedulers.Build("interval", scheduler.Options{Interval: gap})
}
