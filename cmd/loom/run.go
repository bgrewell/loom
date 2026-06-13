// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/flow"
	"github.com/bgrewell/loom/core/generator"
	"github.com/bgrewell/loom/core/report"
	"github.com/bgrewell/loom/core/scheduler"
	"github.com/bgrewell/stencil"
)

// runCommand builds the `loom run` subcommand: the iperf-esque single-flow path.
func runCommand() *stencil.Command {
	fs := stencil.NewFlagSet()
	fs.String("generator", "g", "traffic generator", "stream")
	fs.String("payload", "", "payload source: random|patterned", "random")
	fs.String("datapath", "d", "datapath: discard|udp|tcp|memory", "discard")
	fs.String("target", "t", "destination host:port (for udp/tcp)", "")
	fs.Int("packet-size", "s", "packet size in bytes", 1400)
	fs.String("rate", "r", "send rate, e.g. 100Mbps (empty = unlimited)", "")
	fs.Duration("duration", "", "run duration", 10*time.Second)
	fs.Int("count", "", "stop after N packets (0 = off)", 0)
	fs.String("bytes", "", "stop after N bytes, e.g. 100MB (empty = off)", "")
	fs.Duration("interval", "i", "report interval", time.Second)
	fs.String("output", "o", "report format: human|json", "human")

	return &stencil.Command{
		Name:    "run",
		Summary: "Run a single traffic flow and report throughput",
		Long:    "Build a flow from flags and run it, printing streaming interval reports and an end-of-run summary.",
		Flags:   fs,
		Run:     runFlow,
	}
}

func runFlow(ctx *stencil.Context) error {
	f := ctx.Flags
	pkt := f.Int("packet-size")

	gen, err := generator.Registry.Build(f.String("generator"), generator.Options{
		Payload:    f.String("payload"),
		PacketSize: pkt,
	})
	if err != nil {
		return err
	}

	sched, err := schedulerFor(f.String("rate"), pkt)
	if err != nil {
		return err
	}

	dp, err := datapath.Registry.Build(f.String("datapath"), datapath.Options{Addr: f.String("target")})
	if err != nil {
		return err
	}
	defer dp.Close()

	stop := flow.Stop{
		After: f.Duration("duration"),
		Count: uint64(f.Int("count")),
	}
	if bs := f.String("bytes"); bs != "" {
		v, err := parseBytes(bs)
		if err != nil {
			return err
		}
		stop.Volume = v
	}

	fl := &flow.Flow{Generator: gen, Scheduler: sched, Datapath: dp, MTU: pkt, Stop: stop}

	var rep report.Reporter
	if f.String("output") == "json" {
		rep = report.NewJSON(os.Stdout)
	} else {
		rep = report.NewHuman(os.Stdout)
	}

	sigCtx, stopSig := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stopSig()

	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = fl.Run(sigCtx)
		close(done)
	}()

	report.Collect(sigCtx, fl.Counters(), f.Duration("interval"), rep, done)
	<-done // synchronize before reading runErr
	return runErr
}

// schedulerFor returns a soak scheduler for an empty rate, or an interval
// scheduler paced to approximate the given bit rate. (Full rate-grammar parsing
// arrives with #15 / go-conversions; this is a minimal stand-in.)
func schedulerFor(rate string, pkt int) (scheduler.Scheduler, error) {
	if strings.TrimSpace(rate) == "" {
		return scheduler.Soak{}, nil
	}
	bps, err := parseBitRate(rate)
	if err != nil {
		return nil, err
	}
	if pkt < 1 {
		pkt = 1
	}
	pps := bps / float64(pkt*8)
	if pps <= 0 {
		return nil, fmt.Errorf("rate %q too low for packet size %d", rate, pkt)
	}
	gap := time.Duration(float64(time.Second) / pps)
	if gap < 1 {
		gap = 1
	}
	return scheduler.Registry.Build("interval", scheduler.Options{Interval: gap})
}

// parseBitRate parses "100", "100K", "100M", "1Gbps" → bits/sec.
func parseBitRate(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "bps")
	s = strings.TrimSuffix(s, "b")
	mult := 1.0
	if n := len(s); n > 0 {
		switch s[n-1] {
		case 'k', 'K':
			mult, s = 1e3, s[:n-1]
		case 'm', 'M':
			mult, s = 1e6, s[:n-1]
		case 'g', 'G':
			mult, s = 1e9, s[:n-1]
		}
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("invalid rate %q", s)
	}
	return v * mult, nil
}

// parseBytes parses "1000", "100K", "100MB", "1GB" → bytes (binary multipliers).
func parseBytes(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "B")
	mult := uint64(1)
	if n := len(s); n > 0 {
		switch s[n-1] {
		case 'k', 'K':
			mult, s = 1024, s[:n-1]
		case 'm', 'M':
			mult, s = 1024*1024, s[:n-1]
		case 'g', 'G':
			mult, s = 1024*1024*1024, s[:n-1]
		}
	}
	v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid byte count %q", s)
	}
	return v * mult, nil
}
