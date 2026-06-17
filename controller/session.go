// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// RunSession drives the standard run lifecycle for an already-wired controller and
// telemetry collector, and returns the authoritative end-of-run aggregate plus the
// wall-clock duration of the run.
//
// It mirrors the sequence the loomctl CLI uses: time-sync the agents, start the
// telemetry collector, run the timeline, wait for the traffic sources to finish
// (with a short drain for trailing bytes), tear the flows down, snapshot, and stop
// the collector. The caller owns rendering — observers must already be attached to
// tel — so this is shared by both the scenario CLI and the iperf-style `loom`
// front end without either reimplementing the ordering.
func RunSession(ctx context.Context, c *Controller, tel *Telemetry, horizon time.Duration) (Aggregate, time.Duration, error) {
	// Time-sync each agent up front so clock offsets are known before traffic flows
	// (the gate/consolidation depends on it). Best-effort: a failure is a warning.
	syncCtx, syncCancel := context.WithTimeout(ctx, 5*time.Second)
	if samples, err := c.SyncAgents(syncCtx); err != nil {
		fmt.Fprintf(os.Stderr, "warning: time-sync failed: %v\n", err)
	} else {
		for endpoint, s := range samples {
			fmt.Fprintf(os.Stderr, "time-sync %s: offset %v, delay %v\n", endpoint, s.Offset, s.Delay)
		}
	}
	syncCancel()

	runCtx, cancel := context.WithTimeout(ctx, horizon)
	defer cancel()

	collectDone := make(chan struct{})
	go func() { tel.Collect(runCtx, c); close(collectDone) }()

	runStart := time.Now()
	if err := c.Run(runCtx, horizon); err != nil && !errors.Is(err, context.Canceled) {
		cancel()
		<-collectDone
		return Aggregate{}, 0, err
	}

	// Stop as soon as the sources finish rather than idling to the horizon; an
	// unbounded run instead waits for the horizon or cancellation. A short drain
	// improves trailing-byte accuracy in the totals (not the line count).
	if tel.WaitSources(runCtx, c) {
		time.Sleep(250 * time.Millisecond)
	}
	c.Teardown(context.Background())   // stop flows; flush their final cumulative samples
	time.Sleep(150 * time.Millisecond) // let the collector ingest those final samples
	snap := tel.Snapshot()
	cancel() // stop the collector
	<-collectDone
	return snap, time.Since(runStart), nil
}
