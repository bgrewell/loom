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
	// Frame carries the L2/L3/L4 addressing for the "ethernet" generator (raw
	// datapaths). Nil for stack-based datapaths, where the kernel builds headers.
	Frame *FrameOptions
}

const defaultPacketSize = 1400

// Registry holds the available generator factories by name.
var Registry = registry.New[Generator, Options]()

func buildPayload(o Options) (payload.Payloader, int, error) {
	name := o.Payload
	if name == "" {
		name = "random"
	}
	size := o.PacketSize
	if size <= 0 {
		size = defaultPacketSize
	}
	pl, err := payload.Registry.Build(name, payload.Options{Size: size, Seed: o.Seed})
	return pl, size, err
}

func init() {
	Registry.Register("stream", func(o Options) (Generator, error) {
		pl, size, err := buildPayload(o)
		if err != nil {
			return nil, err
		}
		return NewStream(pl, size), nil
	})
	// ethernet crafts complete Ethernet/IPv4/UDP frames for raw datapaths (AF_XDP).
	Registry.Register("ethernet", func(o Options) (Generator, error) {
		pl, size, err := buildPayload(o)
		if err != nil {
			return nil, err
		}
		return NewEthernet(o.Frame, pl, size)
	})
}
