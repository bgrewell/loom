// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"net"
	"testing"
	"time"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
	"github.com/bgrewell/loom/control"
	"github.com/bgrewell/loom/core/scenario"
)

// startAgent runs an in-process control server and returns its address + a stop.
func startAgent(t *testing.T) (addr string, stop func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := control.NewServer("agent")
	srv.SetTelemetryInterval(10 * time.Millisecond)
	gs := control.NewGRPCServer(srv)
	go func() { _ = gs.Serve(lis) }()
	return lis.Addr().String(), gs.Stop
}

// TestControllerDrivesMultiAgentFlow loads a one-event scenario and drives it
// across two agents — the controller resolves endpoints, configures a receiver
// and a sender, and starts them. We then confirm the receiver agent accounted
// inbound traffic.
func TestControllerDrivesMultiAgentFlow(t *testing.T) {
	clientAddr, stopClient := startAgent(t)
	defer stopClient()
	serverAddr, stopServer := startAgent(t)
	defer stopServer()

	s := &scenario.Scenario{
		Name: "two-node",
		Seed: 7,
		Endpoints: []scenario.Endpoint{
			{Name: "client"},
			{Name: "server"},
		},
		Timeline: []scenario.Event{
			{
				Name:  "udp-blast",
				Flow:  scenario.Flow{Kind: "udp", Params: map[string]any{"packet_size": 1000}},
				From:  scenario.Selector{Raw: "client"},
				To:    scenario.Selector{Raw: "server"},
				Start: scenario.Start{Offset: 0},
				Stop:  scenario.Stop{Count: 2000},
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
		t.Fatalf("expected 2 placed flows (sender+receiver), got %d", len(placed))
	}

	// Let the flow run, then read the receiver's accounting.
	time.Sleep(200 * time.Millisecond)
	var rx Placed
	found := false
	for _, p := range placed {
		if p.Role == Receiver {
			rx, found = p, true
		}
	}
	if !found {
		t.Fatal("no receiver flow was placed")
	}

	stream, err := rx.Agent.StreamTelemetry(ctx, &loomv1.TelemetryRequest{FlowId: rx.FlowID})
	if err != nil {
		t.Fatalf("receiver StreamTelemetry: %v", err)
	}
	sample, err := stream.Recv()
	if err != nil {
		t.Fatalf("receiver Recv: %v", err)
	}
	if sample.GetPackets() == 0 || sample.GetBytes() == 0 {
		t.Fatalf("receiver accounted nothing: %+v", sample)
	}
	t.Logf("scenario-driven flow: receiver accounted %d packets / %d bytes", sample.GetPackets(), sample.GetBytes())

	c.Teardown(ctx)
}
