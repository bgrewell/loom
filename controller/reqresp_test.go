// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"testing"
	"time"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
	"github.com/bgrewell/loom/core/scenario"
)

func TestRequesterSpec(t *testing.T) {
	ev := scenario.Event{
		Name: "browse",
		Flow: scenario.Flow{Kind: "https-browse", Params: map[string]any{"object_size": "1000", "objects": 5}},
		Stop: scenario.Stop{Count: 5},
	}
	s := requesterSpec(ev, "tcp", "10.0.0.2:8080", 42)
	if s.GetRole() != loomv1.FlowRole_FLOW_ROLE_REQUESTER {
		t.Errorf("role = %v, want REQUESTER", s.GetRole())
	}
	if s.GetTransport() != "tcp" || s.GetTarget() != "10.0.0.2:8080" {
		t.Errorf("transport/target = %q/%q", s.GetTransport(), s.GetTarget())
	}
	if s.GetEmulation() != "https-browse" {
		t.Errorf("emulation = %q, want https-browse", s.GetEmulation())
	}
	if s.GetParams()["object_size"] != "1000" || s.GetSeed() != 42 || s.GetCount() != 5 {
		t.Errorf("params/seed/count = %v/%d/%d", s.GetParams(), s.GetSeed(), s.GetCount())
	}
}

// TestControllerDrivesRequestResponse loads a one-event https-browse scenario and
// confirms the controller places a responder + requester pair and the responder
// serves the download.
func TestControllerDrivesRequestResponse(t *testing.T) {
	clientAddr, stopClient := startAgent(t)
	defer stopClient()
	serverAddr, stopServer := startAgent(t)
	defer stopServer()

	s := &scenario.Scenario{
		Name: "browse",
		Seed: 7,
		Endpoints: []scenario.Endpoint{
			{Name: "client"},
			{Name: "server"},
		},
		Timeline: []scenario.Event{
			{
				Name: "web",
				Flow: scenario.Flow{Kind: "https-browse", Params: map[string]any{
					"object_size": "1000", "objects": 5, "think": "0", "packet_size": 1400,
				}},
				From:  scenario.Selector{Raw: "client"},
				To:    scenario.Selector{Raw: "server"},
				Start: scenario.Start{Offset: 0},
				Stop:  scenario.Stop{Count: 5},
			},
		},
	}

	c := New(s, map[string]string{"client": clientAddr, "server": serverAddr})
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := c.Run(ctx, time.Second); err != nil {
		t.Fatalf("controller Run: %v", err)
	}

	placed := c.Placed()
	if len(placed) != 2 {
		t.Fatalf("expected 2 placed flows (responder+requester), got %d", len(placed))
	}
	var resp Placed
	roles := map[Role]bool{}
	for _, p := range placed {
		roles[p.Role] = true
		if p.Role == Responder {
			resp = p
		}
	}
	if !roles[Responder] || !roles[Requester] {
		t.Fatalf("expected a responder and a requester, got roles %+v", roles)
	}

	// Let the exchange run, then read the responder's accounting.
	time.Sleep(300 * time.Millisecond)
	stream, err := resp.Agent.StreamTelemetry(ctx, &loomv1.TelemetryRequest{FlowId: resp.FlowID})
	if err != nil {
		t.Fatalf("responder StreamTelemetry: %v", err)
	}
	sample, err := stream.Recv()
	if err != nil {
		t.Fatalf("responder Recv: %v", err)
	}
	if sample.GetBytes() == 0 {
		t.Fatalf("responder served nothing: %+v", sample)
	}
	t.Logf("scenario-driven request/response: responder served %d bytes", sample.GetBytes())

	c.Teardown(ctx)
}
