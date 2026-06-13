// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package timeline turns a scenario's timeline into a schedule of "fires" — when
// each event (and each repeat instance) should start — and drives them in real
// time. Randomized intervals are reproducible: the RNG is seeded per event so
// adding one event doesn't perturb another's stream. See DESIGN.md §9.
package timeline

import (
	"context"
	"fmt"
	"hash/fnv"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/bgrewell/loom/core/scenario"
	"github.com/bgrewell/loom/core/units"
)

// Fire is one scheduled event instance.
type Fire struct {
	Event string        // event name
	Index int           // repeat instance index (0 for single events)
	At    time.Duration // offset from scenario start
}

// Plan computes all fires up to horizon, deterministically given the scenario
// seed. Events without a repeat fire once at their start; repeating events fire
// at start and then at randomized intervals until count/until/horizon.
func Plan(s *scenario.Scenario, horizon time.Duration) ([]Fire, error) {
	var fires []Fire
	for _, ev := range s.Timeline {
		start := ev.Start.Offset
		if ev.Repeat == nil {
			if start <= horizon {
				fires = append(fires, Fire{Event: ev.Name, At: start})
			}
			continue
		}

		ir, err := units.ParseDurationRange(ev.Repeat.Interval)
		if err != nil {
			return nil, fmt.Errorf("event %q repeat.interval: %w", ev.Name, err)
		}
		end := horizon
		if ev.Repeat.Until != "" {
			if u, err := units.ParseDuration(strings.TrimPrefix(ev.Repeat.Until, "+")); err == nil && u < end {
				end = u
			}
		}

		r := rngFor(s.Seed, ev.Name)
		t := start
		for idx := 0; t <= end; idx++ {
			fires = append(fires, Fire{Event: ev.Name, Index: idx, At: t})
			if ev.Repeat.Count > 0 && idx+1 >= ev.Repeat.Count {
				break
			}
			t += sample(r, ir)
		}
	}
	sort.SliceStable(fires, func(i, j int) bool { return fires[i].At < fires[j].At })
	return fires, nil
}

// Run drives fires in real time relative to start, calling onFire for each, until
// ctx is cancelled or all fires complete.
func Run(ctx context.Context, fires []Fire, start time.Time, onFire func(Fire)) {
	for _, f := range fires {
		wait := time.Until(start.Add(f.At))
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		} else if ctx.Err() != nil {
			return
		}
		onFire(f)
	}
}

func sample(r *rand.Rand, d units.DurationRange) time.Duration {
	if d.Hi <= d.Lo {
		return d.Lo
	}
	return d.Lo + time.Duration(r.Int63n(int64(d.Hi-d.Lo)+1))
}

// rngFor returns a per-event RNG so each event's randomness is independent and
// reproducible from the scenario seed.
func rngFor(seed int64, name string) *rand.Rand {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return rand.New(rand.NewSource(seed ^ int64(h.Sum64())))
}
