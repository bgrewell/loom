// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"testing"
	"time"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestHealthReportsAPIVersion(t *testing.T) {
	client, _, stop := dialAgent(t, func(*Server) {})
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	h, err := client.Health(ctx, &loomv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.GetApiVersion() != APIVersion {
		t.Fatalf("api_version = %d, want %d", h.GetApiVersion(), APIVersion)
	}
}

func TestConfigureRoleSemantics(t *testing.T) {
	client, _, stop := dialAgent(t, func(*Server) {})
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Unspecified role defaults to a sender (no data port bound).
	sender := &loomv1.FlowSpec{
		Generator: "stream", Payload: "random", Datapath: "discard",
		PacketSize: 1000, Duration: durationpb.New(50 * time.Millisecond),
	}
	cfg, err := client.Configure(ctx, &loomv1.ConfigureRequest{Flow: sender})
	if err != nil {
		t.Fatalf("Configure sender (unspecified role): %v", err)
	}
	if cfg.GetDataPort() != 0 {
		t.Fatalf("sender data_port = %d, want 0", cfg.GetDataPort())
	}

	// Receiver role binds an ephemeral port.
	rxCfg, err := client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role: loomv1.FlowRole_FLOW_ROLE_RECEIVER, Datapath: "udp", PacketSize: 1000,
	}})
	if err != nil || rxCfg.GetDataPort() == 0 {
		t.Fatalf("Configure receiver = %+v, %v", rxCfg, err)
	}

	// Reflector is not implemented yet.
	_, err = client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role: loomv1.FlowRole_FLOW_ROLE_REFLECTOR, Datapath: "udp", PacketSize: 1000,
	}})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("reflector Configure = %v, want Unimplemented", err)
	}
}
