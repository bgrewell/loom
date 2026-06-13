// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package payload_test

import (
	"testing"

	"github.com/bgrewell/loom/core/contract"
	"github.com/bgrewell/loom/core/payload"
)

func TestPayloaderContract(t *testing.T) {
	contract.Payloader(t, payload.NewRandom(64, 1))
	contract.Payloader(t, payload.NewPatterned())
}
