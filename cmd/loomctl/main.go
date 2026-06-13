// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Command loomctl is the loom controller — it loads a scenario and drives it
// across agents (DESIGN.md §11). Telemetry aggregation/reporting is a follow-on.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/bgrewell/loom/controller"
	"github.com/bgrewell/loom/core/scenario"
	"github.com/bgrewell/stencil"
)

var version = "dev"

func main() {
	app := stencil.NewApp(
		stencil.WithName("loomctl"),
		stencil.WithDescription("loom controller — run a scenario across agents"),
		stencil.WithVersionInfo(stencil.VersionInfo{Version: version}),
	)
	app.Root.Sub = append(app.Root.Sub, runCommand())
	os.Exit(app.Execute(os.Args[1:]))
}

func runCommand() *stencil.Command {
	fs := stencil.NewFlagSet()
	fs.String("scenario", "f", "scenario YAML file", "")
	fs.StringSlice("agent", "a", "endpoint=host:port pairs, comma-separated", nil)
	fs.Duration("horizon", "", "timeline horizon", 30*time.Second)
	fs.Bool("live", "l", "stream live aggregate telemetry", true)
	fs.Duration("interval", "i", "telemetry interval", time.Second)
	fs.String("output", "o", "telemetry format: human|json", "human")
	return &stencil.Command{
		Name:    "run",
		Summary: "Run a scenario file across agents",
		Flags:   fs,
		Run:     runScenario,
	}
}

func runScenario(ctx *stencil.Context) error {
	path := ctx.Flags.String("scenario")
	if path == "" {
		return fmt.Errorf("a scenario file is required (--scenario/-f)")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sc, err := scenario.Parse(data)
	if err != nil {
		return err
	}

	addrs := make(map[string]string)
	for _, a := range ctx.Flags.StringSlice("agent") {
		k, v, ok := strings.Cut(a, "=")
		if !ok {
			return fmt.Errorf("bad --agent %q (want endpoint=host:port)", a)
		}
		addrs[k] = v
	}

	c := controller.New(sc, addrs)
	defer c.Close()

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	horizon := ctx.Flags.Duration("horizon")
	runCtx, cancel := context.WithTimeout(sigCtx, horizon)
	defer cancel()

	// Optional realtime telemetry: the controller streams per-flow samples from
	// the agents; observers render them (CLI here, an API/dashboard later).
	if ctx.Flags.Bool("live") {
		tel := controller.NewTelemetry(ctx.Flags.Duration("interval"))
		if ctx.Flags.String("output") == "json" {
			tel.AddObserver(controller.NewJSONObserver(os.Stdout))
		} else {
			tel.AddObserver(controller.NewTextObserver(os.Stdout))
		}
		go tel.Collect(runCtx, c)
	}

	fmt.Fprintf(os.Stderr, "running scenario %q across %d agents\n", sc.Name, len(addrs))
	if err := c.Run(runCtx, horizon); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	<-runCtx.Done() // keep streaming until horizon or Ctrl-C
	c.Teardown(context.Background())
	fmt.Fprintf(os.Stderr, "done: placed %d flows\n", len(c.Placed()))
	return nil
}
