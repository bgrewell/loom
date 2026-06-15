// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"testing"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
	"github.com/bgrewell/loom/core/components"
	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/generator"
	"github.com/bgrewell/loom/core/payload"
	"github.com/bgrewell/loom/core/registry"
	"github.com/bgrewell/loom/core/scheduler"
)

// TestCapabilitiesFromInjectedComponents: an agent advertises exactly the parts
// of its injected Components, not the global registries (ADR-0022 per-agent
// capability truth).
func TestCapabilitiesFromInjectedComponents(t *testing.T) {
	c := &components.Components{
		TxDatapaths: registry.New[datapath.TxDatapath, datapath.Options](),
		RxDatapaths: registry.New[datapath.RxDatapath, datapath.Options](),
		Generators:  registry.New[generator.Generator, generator.Options](),
		Schedulers:  registry.New[scheduler.Scheduler, scheduler.Options](),
		Payloads:    registry.New[payload.Payloader, payload.Options](),
	}
	c.TxDatapaths.Register("only-tx", func(datapath.Options) (datapath.TxDatapath, error) {
		return datapath.NewDiscard(1500), nil
	})

	srv := NewServer("t", WithComponents(c))
	resp, err := srv.Capabilities(context.Background(), &loomv1.CapabilitiesRequest{})
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if got := resp.GetDatapaths(); len(got) != 1 || got[0] != "only-tx" {
		t.Fatalf("datapaths = %v, want [only-tx]", got)
	}
	if len(resp.GetGenerators()) != 0 || len(resp.GetPayloads()) != 0 {
		t.Fatalf("expected empty generators/payloads, got %v/%v", resp.GetGenerators(), resp.GetPayloads())
	}
}
