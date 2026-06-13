// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"time"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
	"github.com/bgrewell/loom/core/timesync"
)

// Sync performs one four-timestamp time-sync exchange against an agent and
// returns the estimated clock offset and round-trip delay. The caller stamps t1
// before the RPC and t4 after it; the agent stamps t2/t3 (see [Server.TimeSync]
// and the timesync package).
func Sync(ctx context.Context, c loomv1.ControlClient) (timesync.Sample, error) {
	t1 := time.Now().UnixNano()
	resp, err := c.TimeSync(ctx, &loomv1.TimeSyncRequest{T1: t1})
	if err != nil {
		return timesync.Sample{}, err
	}
	t4 := time.Now().UnixNano()
	return timesync.NewSample(resp.GetT1(), resp.GetT2(), resp.GetT3(), t4), nil
}
