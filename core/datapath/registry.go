// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import "github.com/bgrewell/loom/core/registry"

// Options configures a datapath built through the registry.
type Options struct {
	// Size is the buffer depth for the "memory" datapath.
	Size int
	// Addr is the dial target (host:port) for the "udp"/"tcp" datapaths.
	Addr string
}

// defaultMemorySize is used when Options.Size is unset for the memory datapath.
const defaultMemorySize = 1024

// Registry holds the available datapath factories by name.
var Registry = registry.New[Datapath, Options]()

func init() {
	Registry.Register("memory", func(o Options) (Datapath, error) {
		size := o.Size
		if size <= 0 {
			size = defaultMemorySize
		}
		return NewMemory(size), nil
	})
	Registry.Register("udp", func(o Options) (Datapath, error) {
		s, err := DialUDP(o.Addr)
		if err != nil {
			return nil, err
		}
		return s, nil
	})
	Registry.Register("tcp", func(o Options) (Datapath, error) {
		s, err := DialTCP(o.Addr)
		if err != nil {
			return nil, err
		}
		return s, nil
	})
}
