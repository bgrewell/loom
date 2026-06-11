// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package scheduler

import "context"

// Soak sends as fast as the datapath allows, with no pacing.
type Soak struct{}

// Name implements Scheduler.
func (Soak) Name() string { return "soak" }

// Pace returns immediately unless the context is done.
func (Soak) Pace(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	default:
		return true
	}
}
