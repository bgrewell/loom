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

// startAuthedAgent starts an in-process agent requiring token, returning its
// address and a stop func.
func startAuthedAgent(t *testing.T, token string) (string, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := NewServer("test-1.0", WithAuthToken(token))
	gs := NewGRPCServer(srv)
	go func() { _ = gs.Serve(lis) }()
	return lis.Addr().String(), func() { gs.Stop() }
}

func TestAuthTokenEnforced(t *testing.T) {
	addr, stop := startAuthedAgent(t, "s3cret")
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tests := []struct {
		name string
		opts []DialOption
		want codes.Code
	}{
		{"no token", nil, codes.Unauthenticated},
		{"wrong token", []DialOption{WithToken("nope")}, codes.Unauthenticated},
		{"correct token", []DialOption{WithToken("s3cret")}, codes.OK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, conn, err := Dial(addr, tt.opts...)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()
			_, err = client.Health(ctx, &loomv1.HealthRequest{})
			if got := status.Code(err); got != tt.want {
				t.Fatalf("Health code = %v, want %v (err=%v)", got, tt.want, err)
			}
		})
	}
}

// TestAuthStreamEnforced confirms the stream interceptor also rejects an
// unauthenticated StreamTelemetry (a long-lived stream, separate code path).
func TestAuthStreamEnforced(t *testing.T) {
	addr, stop := startAuthedAgent(t, "s3cret")
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, conn, err := Dial(addr) // no token
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	stream, err := client.StreamTelemetry(ctx, &loomv1.TelemetryRequest{FlowId: "flow-1"})
	if err == nil {
		_, err = stream.Recv() // stream errors surface on first Recv
	}
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("StreamTelemetry code = %v, want Unauthenticated (err=%v)", got, err)
	}
}

// TestAuthDisabledByDefault: with no token set, the plane is open (backward
// compatible for loopback/dev use).
func TestAuthDisabledByDefault(t *testing.T) {
	srv := NewServer("test-1.0")
	if srv.AuthEnabled() {
		t.Fatal("auth should be disabled by default")
	}
}
