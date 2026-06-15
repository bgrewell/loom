// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	"github.com/bgrewell/loom/control"
	"github.com/bgrewell/loom/core/timesync"
)

// SyncAgents performs a time-sync exchange with each endpoint's agent and
// returns the per-endpoint offset/delay. It reuses the controller's control
// connections (dialing any not yet connected). A failure against one agent is
// returned immediately; partial results gathered so far are still returned.
func (c *Controller) SyncAgents(ctx context.Context) (map[string]timesync.Sample, error) {
	out := make(map[string]timesync.Sample, len(c.addrs))
	for endpoint := range c.addrs {
		cl, _, err := c.agentFor(endpoint)
		if err != nil {
			return out, err
		}
		s, err := control.Sync(ctx, cl)
		if err != nil {
			return out, fmt.Errorf("timesync %q: %w", endpoint, err)
		}
		out[endpoint] = s
	}
	// Remember the offsets/delays so fire() can schedule a shared start time
	// translated into each agent's clock (the scheduled-start gate).
	c.mu.Lock()
	for endpoint, s := range out {
		c.sync[endpoint] = s
	}
	c.mu.Unlock()
	return out, nil
}
