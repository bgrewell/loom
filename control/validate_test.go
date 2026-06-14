// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"net"
	"testing"
	"time"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestValidatePacketSize(t *testing.T) {
	tests := []struct {
		name string
		n    uint32
		ok   bool
	}{
		{"zero", 0, false},
		{"min", 1, true},
		{"typical", 1400, true},
		{"max", maxPacketSize, true},
		{"over max", maxPacketSize + 1, false},
		{"uint32 max", ^uint32(0), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validatePacketSize(tt.n); (err == nil) != tt.ok {
				t.Errorf("validatePacketSize(%d) err=%v, want ok=%v", tt.n, err, tt.ok)
			}
		})
	}
}

func TestValidateTarget(t *testing.T) {
	tests := []struct {
		name   string
		target string
		ok     bool
	}{
		{"empty allowed", "", true},
		{"host:port", "10.0.0.1:9000", true},
		{"named port", "host.example:domain", true},
		{"no port", "10.0.0.1", false},
		{"empty host", ":9000", false},
		{"bad port", "10.0.0.1:0", false},
		{"garbage", "not a target", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateTarget(tt.target); (err == nil) != tt.ok {
				t.Errorf("validateTarget(%q) err=%v, want ok=%v", tt.target, err, tt.ok)
			}
		})
	}
}

// TestConfigureRejectsOversizedPacket: a packet_size beyond the cap is refused
// with InvalidArgument instead of attempting a multi-gigabyte allocation.
func TestConfigureRejectsOversizedPacket(t *testing.T) {
	client, _, stop := dialAgent(t, func(*Server) {})
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Generator: "stream", Payload: "random", Datapath: "discard",
		PacketSize: ^uint32(0), // ~4 GiB
	}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Configure oversized = %v, want InvalidArgument", err)
	}
}

// TestConfigureFlowLimit: past the configured cap, Configure returns
// ResourceExhausted rather than binding more ports / allocating more flows.
func TestConfigureFlowLimit(t *testing.T) {
	client, _, stop := dialAgent(t, func(s *Server) { s.SetMaxFlows(2) })
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mk := func() error {
		_, err := client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
			Generator: "stream", Payload: "random", Datapath: "discard", PacketSize: 1000, Count: 1,
		}})
		return err
	}
	if err := mk(); err != nil {
		t.Fatalf("first Configure: %v", err)
	}
	if err := mk(); err != nil {
		t.Fatalf("second Configure: %v", err)
	}
	if err := mk(); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("third Configure = %v, want ResourceExhausted", err)
	}
}

// dialAgent starts an in-process agent (configured via cfg) and returns a
// connected client plus a stop func.
func dialAgent(t *testing.T, cfg func(*Server)) (loomv1.ControlClient, *Server, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := NewServer("test-1.0")
	cfg(srv)
	gs := NewGRPCServer(srv)
	go func() { _ = gs.Serve(lis) }()
	client, conn, err := Dial(lis.Addr().String())
	if err != nil {
		gs.Stop()
		t.Fatalf("dial: %v", err)
	}
	return client, srv, func() { _ = conn.Close(); gs.Stop() }
}
