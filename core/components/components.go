// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package components bundles the pluggable registries (datapath/generator/
// scheduler/payload/netpath) into one injectable value (ADR-0022), so a flow builder or
// an agent depends on an explicit Components rather than reaching into global
// singletons. Default() returns the standard set backed by the package
// registries — which the built-in factories (and build-tagged backends like
// afxdp) still self-register into via init — so injection is additive: production
// uses Default(), tests inject their own.
package components

import (
	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/generator"
	"github.com/bgrewell/loom/core/netpath"
	"github.com/bgrewell/loom/core/payload"
	"github.com/bgrewell/loom/core/registry"
	"github.com/bgrewell/loom/core/scheduler"
)

// Components holds the registries a flow/agent resolves its parts from.
type Components struct {
	TxDatapaths *registry.Registry[datapath.TxDatapath, datapath.Options]
	RxDatapaths *registry.Registry[datapath.RxDatapath, datapath.Options]
	Generators  *registry.Registry[generator.Generator, generator.Options]
	Schedulers  *registry.Registry[scheduler.Scheduler, scheduler.Options]
	Payloads    *registry.Registry[payload.Payloader, payload.Options]
	Networks    *registry.Registry[netpath.Network, netpath.Options]
}

// Default returns the standard component set, backed by the package-level
// registries that the built-in (and build-tagged) factories register into.
func Default() *Components {
	return &Components{
		TxDatapaths: datapath.Registry,
		RxDatapaths: datapath.RxRegistry,
		Generators:  generator.Registry,
		Schedulers:  scheduler.Registry,
		Payloads:    payload.Registry,
		Networks:    netpath.Registry,
	}
}

// OrDefault returns c if non-nil, else Default().
func OrDefault(c *Components) *Components {
	if c != nil {
		return c
	}
	return Default()
}
