// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package generator_test

import (
	"testing"

	"github.com/bgrewell/loom/core/contract"
	"github.com/bgrewell/loom/core/generator"
	"github.com/bgrewell/loom/core/payload"
)

func TestGeneratorContract(t *testing.T) {
	contract.Generator(t, generator.NewStream(payload.NewRandom(2048, 1), 1400))
}
