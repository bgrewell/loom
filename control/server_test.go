// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"net"
	"testing"
	"time"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
)

func TestControlPlane(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gs := NewGRPCServer(NewServer("test-1.0"))
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()

	client, conn, err := Dial(lis.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	h, err := client.Health(ctx, &loomv1.HealthRequest{})
	if err != nil || h.GetVersion() != "test-1.0" || !h.GetReady() {
		t.Fatalf("Health = %+v, %v", h, err)
	}

	t1 := time.Now().UnixNano()
	ts, err := client.TimeSync(ctx, &loomv1.TimeSyncRequest{T1: t1})
	if err != nil || ts.GetT1() != t1 || ts.GetT2() == 0 || ts.GetT3() == 0 {
		t.Fatalf("TimeSync = %+v, %v", ts, err)
	}

	caps, err := client.Capabilities(ctx, &loomv1.CapabilitiesRequest{})
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if !contains(caps.GetDatapaths(), "memory") || !contains(caps.GetGenerators(), "stream") {
		t.Fatalf("capabilities missing expected entries: %+v", caps)
	}

	reg, err := client.Register(ctx, &loomv1.RegisterRequest{AgentId: "agent-7"})
	if err != nil || reg.GetSession() != "agent-7" {
		t.Fatalf("Register = %+v, %v", reg, err)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
