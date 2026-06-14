// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package scheduler

import "context"

// Soak sends as fast as the datapath allows, with no pacing.
type Soak struct{}

// Name implements Scheduler.
func (Soak) Name() string { return "soak" }

// Pace returns max immediately (no pacing) unless the context is done, so the
// pump can send a full batch.
func (Soak) Pace(ctx context.Context, max int) (int, bool) {
	select {
	case <-ctx.Done():
		return 0, false
	default:
		if max < 1 {
			max = 1
		}
		return max, true
	}
}
