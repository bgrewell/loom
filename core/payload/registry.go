// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package payload

import "github.com/bgrewell/loom/core/registry"

// Options configures a payloader built through the registry.
type Options struct {
	// Size is the backing buffer size for the "random" payloader.
	Size int
	// Seed makes the "random" payloader reproducible.
	Seed int64
}

// defaultRandomSize is used when Options.Size is unset.
const defaultRandomSize = 1500

// Registry holds the available payloader factories by name.
var Registry = registry.New[Payloader, Options]()

func init() {
	Registry.Register("random", func(o Options) (Payloader, error) {
		size := o.Size
		if size <= 0 {
			size = defaultRandomSize
		}
		return NewRandom(size, o.Seed), nil
	})
	Registry.Register("patterned", func(Options) (Payloader, error) {
		return NewPatterned(), nil
	})
}
