// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package vidstream

import (
	"time"

	"github.com/bgrewell/loom/core/metrics"
	"github.com/bgrewell/loom/core/rtp"
)

// Metrics implements metrics.Source. Each call syncs the virtual playhead
// and closes one observation interval (the core/app/voip discipline):
// segment/stall/switch counts, stall time, rebuffer ratio (interval stall
// time over interval stall+play time) and average bitrate cover the span
// since the previous Metrics call, and StallEvents lists the stalls
// COMPLETED in the interval — a stall in progress already counts in
// Stalls/StallTimeMs, its event arriving in the interval where it ends (a
// stall still open when Run exits is closed at that instant, see freeze).
// BufferMs is the current buffer level and StartupMs the run's startup delay
// (0 until playback has started), both point-in-time values.
func (c *client) Metrics() metrics.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.syncLocked(c.now())
	v := c.snapshotLocked(
		c.segments-c.mkSegments, c.stalls-c.mkStalls,
		c.upSw-c.mkUp, c.downSw-c.mkDown,
		c.stallTime-c.mkStallTime, c.playTime-c.mkPlayTime,
		c.kbpsDur-c.mkKbpsDur, c.durSum-c.mkDurSum,
		c.events[c.mkEvents:],
	)
	c.mkSegments, c.mkStalls, c.mkUp, c.mkDown = c.segments, c.stalls, c.upSw, c.downSw
	c.mkStallTime, c.mkPlayTime = c.stallTime, c.playTime
	c.mkKbpsDur, c.mkDurSum = c.kbpsDur, c.durSum
	c.mkEvents = len(c.events)
	return v
}

// CumulativeMetrics returns the whole-run snapshot without closing an
// observation interval — the final-sample capability the agent discovers by
// assertion (a run with a mid-stream stall must not summarize as its last
// clean interval).
func (c *client) CumulativeMetrics() metrics.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.syncLocked(c.now())
	return c.snapshotLocked(
		c.segments, c.stalls, c.upSw, c.downSw,
		c.stallTime, c.playTime, c.kbpsDur, c.durSum, c.events,
	)
}

// snapshotLocked assembles a metrics.Video from the given (interval or
// cumulative) aggregates plus the point-in-time buffer level and startup
// delay. Callers hold mu with the playhead synced.
func (c *client) snapshotLocked(segments, stalls, up, down uint64, stallTime, playTime time.Duration, kbpsDur, durSum float64, events []rtp.Gap) metrics.Video {
	v := metrics.Video{
		SegmentsFetched: segments,
		Stalls:          stalls,
		StallTimeMs:     durMs(stallTime),
		BufferMs:        durMs(c.buffer),
		RepSwitchesUp:   up,
		RepSwitchesDown: down,
	}
	if c.startupSet {
		v.StartupMs = durMs(c.startup)
	}
	// RebufferRatio is stall/(stall+play): bounded [0,1] and defined for any
	// interval with elapsed playback time — an interval that is 100% stall
	// honestly reports 1.0 (the stall/play form would be undefined there,
	// and a silent 0 would read as a clean interval at the worst moment).
	if total := stallTime + playTime; total > 0 {
		v.RebufferRatio = float64(stallTime) / float64(total)
	}
	if durSum > 0 {
		v.AvgBitrateKbps = kbpsDur / durSum
	}
	if len(events) > 0 {
		v.StallEvents = append([]rtp.Gap(nil), events...)
	}
	return v
}

// durMs converts a duration to float milliseconds.
func durMs(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
