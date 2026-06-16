// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"fmt"
	"testing"
	"time"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
)

// TestTwoAgentTCPFlow runs a TCP flow from a sender agent to a receiver agent
// over the new TCP datapath, negotiating the receiver's ephemeral port — the path
// that did not exist before (`datapath: tcp` used to fail with no TCP receiver).
func TestTwoAgentTCPFlow(t *testing.T) {
	rx := startAgent(t, "rx")
	defer rx.stop()
	tx := startAgent(t, "tx")
	defer tx.stop()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	rxCfg, err := rx.client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role: loomv1.FlowRole_FLOW_ROLE_RECEIVER, Datapath: "tcp", PacketSize: 1400,
	}})
	if err != nil || rxCfg.GetDataPort() == 0 {
		t.Fatalf("receiver Configure = %+v, %v", rxCfg, err)
	}
	if _, err := rx.client.Start(ctx, &loomv1.StartRequest{FlowId: rxCfg.GetFlowId()}); err != nil {
		t.Fatalf("receiver Start: %v", err)
	}

	target := fmt.Sprintf("127.0.0.1:%d", rxCfg.GetDataPort())
	txCfg, err := tx.client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Datapath: "tcp", Target: target, Generator: "stream", PacketSize: 1400, Count: 5000,
	}})
	if err != nil {
		t.Fatalf("sender Configure: %v", err)
	}
	if _, err := tx.client.Start(ctx, &loomv1.StartRequest{FlowId: txCfg.GetFlowId()}); err != nil {
		t.Fatalf("sender Start: %v", err)
	}

	// Drain the sender's telemetry to completion (count-bounded).
	txStream, err := tx.client.StreamTelemetry(ctx, &loomv1.TelemetryRequest{FlowId: txCfg.GetFlowId()})
	if err != nil {
		t.Fatalf("sender StreamTelemetry: %v", err)
	}
	for {
		if _, err := txStream.Recv(); err != nil {
			break
		}
	}

	// The receiver should have accounted the stream (TCP is lossless, but read it
	// after a short settle so in-flight bytes land).
	time.Sleep(200 * time.Millisecond)
	rxStream, err := rx.client.StreamTelemetry(ctx, &loomv1.TelemetryRequest{FlowId: rxCfg.GetFlowId()})
	if err != nil {
		t.Fatalf("receiver StreamTelemetry: %v", err)
	}
	sample, err := rxStream.Recv()
	if err != nil {
		t.Fatalf("receiver Recv: %v", err)
	}
	if sample.GetBytes() == 0 {
		t.Fatalf("receiver accounted nothing: %+v", sample)
	}
	t.Logf("TCP receiver accounted %d bytes", sample.GetBytes())

	if _, err := rx.client.Stop(ctx, &loomv1.StopRequest{FlowId: rxCfg.GetFlowId()}); err != nil {
		t.Fatalf("receiver Stop: %v", err)
	}
}
