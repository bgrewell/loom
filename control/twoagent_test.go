// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
)

// agentRig is one in-process agent (control server + client). srv is the
// underlying control Server so in-package tests can reach past the wire (e.g.
// to observe a managed flow's engine state the proto does not carry).
type agentRig struct {
	client loomv1.ControlClient
	srv    *Server
	stop   func()
}

func startAgent(t *testing.T, version string) *agentRig {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := NewServer(version)
	srv.telemetry = 5 * time.Millisecond
	gs := NewGRPCServer(srv)
	go func() { _ = gs.Serve(lis) }()

	client, conn, err := Dial(lis.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return &agentRig{
		client: client,
		srv:    srv,
		stop:   func() { conn.Close(); gs.Stop() },
	}
}

// TestTwoAgentFlow runs a real flow from a sender agent to a receiver agent over
// UDP loopback, negotiating the receiver's ephemeral port via Configure.
func TestTwoAgentFlow(t *testing.T) {
	rx := startAgent(t, "rx")
	defer rx.stop()
	tx := startAgent(t, "tx")
	defer tx.stop()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Receiver binds an ephemeral port.
	rxCfg, err := rx.client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role: loomv1.FlowRole_FLOW_ROLE_RECEIVER, Datapath: "udp", PacketSize: 1500,
	}})
	if err != nil || rxCfg.GetDataPort() == 0 {
		t.Fatalf("receiver Configure = %+v, %v", rxCfg, err)
	}
	if _, err := rx.client.Start(ctx, &loomv1.StartRequest{FlowId: rxCfg.GetFlowId()}); err != nil {
		t.Fatalf("receiver Start: %v", err)
	}

	// Sender targets the receiver's port.
	target := fmt.Sprintf("127.0.0.1:%d", rxCfg.GetDataPort())
	txCfg, err := tx.client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Datapath: "udp", Target: target, Generator: "stream", PacketSize: 1000, Count: 2000,
	}})
	if err != nil {
		t.Fatalf("sender Configure: %v", err)
	}
	if _, err := tx.client.Start(ctx, &loomv1.StartRequest{FlowId: txCfg.GetFlowId()}); err != nil {
		t.Fatalf("sender Start: %v", err)
	}

	// Drain the sender's telemetry to completion (its count-bound flow finishes).
	txStream, err := tx.client.StreamTelemetry(ctx, &loomv1.TelemetryRequest{FlowId: txCfg.GetFlowId()})
	if err != nil {
		t.Fatalf("sender StreamTelemetry: %v", err)
	}
	for {
		if _, err := txStream.Recv(); err != nil {
			break
		}
	}

	// Let the receiver finish draining, then read its accounting.
	time.Sleep(150 * time.Millisecond)
	rxStream, err := rx.client.StreamTelemetry(ctx, &loomv1.TelemetryRequest{FlowId: rxCfg.GetFlowId()})
	if err != nil {
		t.Fatalf("receiver StreamTelemetry: %v", err)
	}
	sample, err := rxStream.Recv()
	if err != nil {
		t.Fatalf("receiver Recv: %v", err)
	}
	// UDP loopback at soak rate drops some packets; we just need the receiver to
	// have actually received and accounted traffic from the sender.
	if sample.GetPackets() == 0 || sample.GetBytes() == 0 {
		t.Fatalf("receiver accounted nothing: %+v", sample)
	}
	t.Logf("receiver accounted %d packets / %d bytes (of 2000 sent)", sample.GetPackets(), sample.GetBytes())

	if _, err := rx.client.Stop(ctx, &loomv1.StopRequest{FlowId: rxCfg.GetFlowId()}); err != nil {
		t.Fatalf("receiver Stop: %v", err)
	}
}
