// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"fmt"
	"time"

	"github.com/bgrewell/loom/core/registry"
)

// Options configures a scheduler built through the registry.
type Options struct {
	// Interval is the inter-packet gap for the "interval" scheduler.
	Interval time.Duration
}

// Registry holds the available scheduler factories by name.
var Registry = registry.New[Scheduler, Options]()

func init() {
	Registry.Register("soak", func(Options) (Scheduler, error) {
		return Soak{}, nil
	})
	Registry.Register("interval", func(o Options) (Scheduler, error) {
		if o.Interval <= 0 {
			return nil, fmt.Errorf("scheduler %q requires Interval > 0", "interval")
		}
		return NewInterval(o.Interval), nil
	})
}
