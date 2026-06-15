// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"os/signal"
	"time"

	"github.com/bgrewell/loom/core/flow"
	"github.com/bgrewell/loom/core/report"
	"github.com/bgrewell/loom/core/units"
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

	var volume uint64
	if bs := f.String("bytes"); bs != "" {
		v, err := units.ParseSize(bs)
		if err != nil {
			return err
		}
		volume = v
	}

	fl, err := flow.Build(flow.Spec{
		Generator:  f.String("generator"),
		Payload:    f.String("payload"),
		Datapath:   f.String("datapath"),
		Target:     f.String("target"),
		PacketSize: f.Int("packet-size"),
		Rate:       f.String("rate"),
		Duration:   f.Duration("duration"),
		Count:      uint64(f.Int("count")),
		Volume:     volume,
	}, nil) // nil components = the default registry set
	if err != nil {
		return err
	}
	defer fl.Datapath.Close()

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
