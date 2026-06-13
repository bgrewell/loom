// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath_test

import (
	"testing"

	"github.com/bgrewell/loom/core/contract"
	"github.com/bgrewell/loom/core/datapath"
)

func TestDatapathContract(t *testing.T) {
	contract.Datapath(t, datapath.NewMemory(8))
	contract.Datapath(t, datapath.Discard{})
}
