// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Command loomctl is the loom controller — it loads a scenario and drives it
// across agents (DESIGN.md §11). Telemetry aggregation/reporting is a follow-on.
package main

import (
	"context"
	"encoding/json"
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

// Build metadata, injected at link time via -ldflags (see .goreleaser.yaml).
var (
	version   = "dev"
	buildDate = "unknown"
	commit    = "none"
	branch    = "none"
)

func main() {
	app := stencil.NewApp(
		stencil.WithName("loomctl"),
		stencil.WithDescription("loom controller — run a scenario across agents"),
		stencil.WithVersionInfo(stencil.VersionInfo{
			Version:    version,
			BuildDate:  buildDate,
			CommitHash: commit,
			Branch:     branch,
		}),
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
	fs.Bool("per-flow", "p", "show per-flow throughput (live and in the summary)", false)
	fs.Duration("interval", "i", "telemetry interval", time.Second)
	fs.String("output", "o", "telemetry format: human|json", "human")
	fs.String("token", "t", "control-plane auth token (default $LOOM_TOKEN)", "")
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

	token := ctx.Flags.String("token")
	if token == "" {
		token = os.Getenv("LOOM_TOKEN")
	}

	c := controller.New(sc, addrs, controller.WithToken(token))
	defer c.Close()

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	horizon := ctx.Flags.Duration("horizon")
	runCtx, cancel := context.WithTimeout(sigCtx, horizon)
	defer cancel()

	// Time-sync each agent up front so offsets are known before traffic flows
	// (the seam for one-way-delay measurement, ADR-0010).
	syncCtx, syncCancel := context.WithTimeout(sigCtx, 5*time.Second)
	if samples, err := c.SyncAgents(syncCtx); err != nil {
		fmt.Fprintf(os.Stderr, "warning: time-sync failed: %v\n", err)
	} else {
		for endpoint, s := range samples {
			fmt.Fprintf(os.Stderr, "time-sync %s: offset %v, delay %v\n", endpoint, s.Offset, s.Delay)
		}
	}
	syncCancel()

	// Realtime telemetry: the collector streams per-flow samples from the agents
	// over its own connections (ADR-0013). It always runs — it's how we detect
	// when the traffic sources finish and how we build the end-of-run summary —
	// but it only renders live lines when --live is set (CLI here; an API/dashboard
	// is just another observer later).
	perFlow := ctx.Flags.Bool("per-flow")
	jsonOut := ctx.Flags.String("output") == "json"
	tel := controller.NewTelemetry(ctx.Flags.Duration("interval"), controller.WithTelemetryToken(token))
	defer tel.Close()
	if ctx.Flags.Bool("live") {
		if jsonOut {
			tel.AddObserver(controller.NewJSONObserver(os.Stdout))
		} else {
			tel.AddObserver(controller.NewTextObserver(os.Stdout).WithPerFlow(perFlow))
		}
	}
	collectDone := make(chan struct{})
	go func() { tel.Collect(runCtx, c); close(collectDone) }()

	fmt.Fprintf(os.Stderr, "running scenario %q across %d agents\n", sc.Name, len(addrs))
	runStart := time.Now()
	if err := c.Run(runCtx, horizon); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

	// Stop as soon as the traffic sources finish instead of idling to the horizon.
	// An end-of-test (unbounded) scenario has no completing sources, so this waits
	// for the horizon or Ctrl-C instead.
	waited := tel.WaitSources(runCtx, c)
	// Freeze the live display now so the printed line count equals the run's
	// duration/interval — the drain and teardown below would otherwise add a
	// stray line. The collector keeps ingesting for an accurate final snapshot.
	tel.Freeze()
	if waited {
		time.Sleep(300 * time.Millisecond) // let the receiver drain trailing packets
	}
	c.Teardown(context.Background())   // stop receivers; flush their final samples
	time.Sleep(200 * time.Millisecond) // let the collector ingest those final samples
	elapsed := time.Since(runStart)
	summary := tel.Snapshot()
	cancel() // stop the collector
	<-collectDone

	if jsonOut {
		printJSONSummary(os.Stdout, summary, elapsed)
	} else {
		fmt.Fprint(os.Stdout, summary.Summary(elapsed, perFlow))
	}
	return nil
}

// printJSONSummary writes a final machine-readable summary object.
func printJSONSummary(w *os.File, a controller.Aggregate, elapsed time.Duration) {
	secs := elapsed.Seconds()
	avg := func(bytes uint64) float64 {
		if secs <= 0 {
			return 0
		}
		return float64(bytes) * 8 / secs
	}
	enc := json.NewEncoder(w)
	_ = enc.Encode(map[string]any{
		"summary":         true,
		"elapsed_seconds": secs,
		"tx_bytes":        a.TxBytes,
		"rx_bytes":        a.RxBytes,
		"tx_avg_bps":      avg(a.TxBytes),
		"rx_avg_bps":      avg(a.RxBytes),
		"flows":           len(a.Flows),
	})
}
