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
	// Iface and Queue select the NIC and queue for the "afxdp" datapath.
	Iface string
	Queue int
}

// defaultMemorySize is used when Options.Size is unset for the memory datapath.
const defaultMemorySize = 1024

// Registry holds the available transmit-datapath factories by name.
var Registry = registry.New[TxDatapath, Options]()

// RxRegistry holds the available receive-datapath factories by name, so the
// agent can pick a receiver backend (udp listener, afxdp, …) without importing
// each directly — build-tagged backends register here under their tag.
var RxRegistry = registry.New[RxDatapath, Options]()

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

	// Receive side. ":0" binds an ephemeral port (read back via Port()).
	RxRegistry.Register("udp", func(o Options) (RxDatapath, error) {
		return ListenUDP(":0", o.FrameSize)
	})
	RxRegistry.Register("tcp", func(o Options) (RxDatapath, error) {
		return ListenTCP(":0", o.FrameSize)
	})
}
