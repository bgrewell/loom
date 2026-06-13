// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package generator

import (
	"github.com/bgrewell/loom/core/payload"
	"github.com/bgrewell/loom/core/registry"
)

// Options configures a generator built through the registry.
type Options struct {
	// Payload selects the payload source (default "random").
	Payload string
	// PacketSize is the per-packet size in bytes (default 1400).
	PacketSize int
	// Seed makes a seeded payload reproducible.
	Seed int64
}

const defaultPacketSize = 1400

// Registry holds the available generator factories by name.
var Registry = registry.New[Generator, Options]()

func init() {
	Registry.Register("stream", func(o Options) (Generator, error) {
		name := o.Payload
		if name == "" {
			name = "random"
		}
		pl, err := payload.Registry.Build(name, payload.Options{Size: o.PacketSize, Seed: o.Seed})
		if err != nil {
			return nil, err
		}
		size := o.PacketSize
		if size <= 0 {
			size = defaultPacketSize
		}
		return NewStream(pl, size), nil
	})
}
