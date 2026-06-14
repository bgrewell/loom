// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import "github.com/bgrewell/loom/core/registry"

// Options configures a datapath built through the registry.
type Options struct {
	// Size is the frame-buffer depth for the "memory" datapath.
	Size int
	// FrameSize is the per-frame byte capacity (the flow's packet size).
	FrameSize int
	// Addr is the dial target (host:port) for the "udp"/"tcp" datapaths.
	Addr string
}

// defaultMemorySize is used when Options.Size is unset for the memory datapath.
const defaultMemorySize = 1024

// Registry holds the available transmit-datapath factories by name. Receive-side
// datapaths (e.g. the UDP listener) are constructed directly, not via the
// registry.
var Registry = registry.New[TxDatapath, Options]()

func init() {
	Registry.Register("memory", func(o Options) (TxDatapath, error) {
		size := o.Size
		if size <= 0 {
			size = defaultMemorySize
		}
		return NewMemory(size, o.FrameSize), nil
	})
	Registry.Register("discard", func(o Options) (TxDatapath, error) {
		return NewDiscard(o.FrameSize), nil
	})
	Registry.Register("udp", func(o Options) (TxDatapath, error) {
		return DialUDP(o.Addr, o.FrameSize)
	})
	Registry.Register("tcp", func(o Options) (TxDatapath, error) {
		return DialTCP(o.Addr, o.FrameSize)
	})
}
