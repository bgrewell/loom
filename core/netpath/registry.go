// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package netpath

import (
	"net/netip"

	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/registry"
)

// Options configures a Network built through the registry. Options are pure
// data (registry-safe, ADR-0006 pattern): datapath-backed networks name their
// datapaths and carry datapath.Options rather than live instances, so a
// scenario or agent can describe a network without constructing one.
// Embedders that construct datapaths out-of-band do not go through the
// registry — they call the direct constructors (Host, Memory, …) instead.
type Options struct {
	// Local is the source address for datapath-backed networks, and the
	// optional bind address for "host".
	Local netip.Addr
	// MTU bounds the frame payload size for datapath-backed networks.
	MTU int
	// TxDatapath and RxDatapath name the datapath backends a datapath-backed
	// network resolves from the datapath registries.
	TxDatapath string
	RxDatapath string
	// DatapathOpts configures the named datapaths.
	DatapathOpts datapath.Options
}

// Registry holds the available network factories by name.
var Registry = registry.New[Network, Options]()

func init() {
	Registry.Register("host", func(o Options) (Network, error) {
		return Host(o.Local), nil
	})
	// "memory" builds a single self-connected in-memory network: dialers reach
	// listeners created on the same Network (one handle on a fresh fabric).
	// Tests that want two distinct handles of one fabric call Memory() directly.
	Registry.Register("memory", func(o Options) (Network, error) {
		return newMemFabric().handle(), nil
	})
}
