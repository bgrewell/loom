// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath_test

import (
	"testing"

	"github.com/bgrewell/loom/core/contract"
	"github.com/bgrewell/loom/core/datapath"
)

func TestDatapathContract(t *testing.T) {
	contract.TxDatapath(t, datapath.NewMemory(8, 1500))
	contract.TxDatapath(t, datapath.NewDiscard(1500))
	contract.TxDatapath(t, datapath.NewArena(8, 1500))
	// RX side: memory/arena loopbacks satisfy the receive contract too.
	contract.RxDatapath(t, datapath.NewMemory(8, 1500))
	contract.RxDatapath(t, datapath.NewArena(8, 1500))
}
