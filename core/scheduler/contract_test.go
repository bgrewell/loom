// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package scheduler_test

import (
	"testing"
	"time"

	"github.com/bgrewell/loom/core/contract"
	"github.com/bgrewell/loom/core/scheduler"
)

func TestSchedulerContract(t *testing.T) {
	contract.Scheduler(t, scheduler.Soak{})
	contract.Scheduler(t, scheduler.NewInterval(time.Millisecond))
}
