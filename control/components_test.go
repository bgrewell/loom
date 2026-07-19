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

// TestConfigureWithPartialComponents: a Components literal that predates the
// Networks registry (the field is nil) must not crash Configure — the agent
// falls back to the kernel stack instead of dereferencing a nil registry.
func TestConfigureWithPartialComponents(t *testing.T) {
	c := &components.Components{
		TxDatapaths: registry.New[datapath.TxDatapath, datapath.Options](),
		RxDatapaths: registry.New[datapath.RxDatapath, datapath.Options](),
		Generators:  registry.New[generator.Generator, generator.Options](),
		Schedulers:  registry.New[scheduler.Scheduler, scheduler.Options](),
		Payloads:    registry.New[payload.Payloader, payload.Options](),
		// Networks intentionally nil: pre-existing embedder pattern.
	}
	srv := NewServer("t", WithComponents(c))
	resp, err := srv.Configure(context.Background(), &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role:       loomv1.FlowRole_FLOW_ROLE_RESPONDER,
		Transport:  "udp",
		PacketSize: 512,
	}})
	if err != nil {
		t.Fatalf("Configure(RESPONDER) with nil Networks: %v", err)
	}
	if resp.GetDataPort() == 0 {
		t.Error("responder reported data_port 0, want a bound ephemeral port")
	}
	if _, err := srv.Destroy(context.Background(), &loomv1.DestroyRequest{FlowId: resp.GetFlowId()}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
}
