// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"crypto/subtle"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// authMetadataKey is the gRPC metadata key carrying the bearer token.
const authMetadataKey = "authorization"

// Control-plane auth (ADR-0014): a single optional shared token authenticates the
// connection; once authenticated it is trusted (no roles/RBAC). Transport
// encryption / mutual identity via mTLS is a separate, still-optional layer. When
// no token is configured the interceptors are not installed and the plane is
// open (suitable only for loopback / trusted networks).

// tokenCreds attaches a bearer token to every outbound RPC.
type tokenCreds struct{ token string }

// GetRequestMetadata implements credentials.PerRPCCredentials.
func (c tokenCreds) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{authMetadataKey: "Bearer " + c.token}, nil
}

// RequireTransportSecurity reports whether the transport must be secure. It is
// false because token auth is supported over the current insecure transport;
// mTLS, when added, supplies transport security independently.
func (c tokenCreds) RequireTransportSecurity() bool { return false }

// verifyToken checks the request metadata carries the expected bearer token,
// using a constant-time comparison to avoid leaking it via timing.
func verifyToken(ctx context.Context, want string) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing credentials")
	}
	vals := md.Get(authMetadataKey)
	if len(vals) == 0 {
		return status.Error(codes.Unauthenticated, "missing auth token")
	}
	got := strings.TrimPrefix(vals[0], "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return status.Error(codes.Unauthenticated, "invalid auth token")
	}
	return nil
}

// tokenUnaryInterceptor enforces the shared token on every unary RPC.
func tokenUnaryInterceptor(token string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := verifyToken(ctx, token); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// tokenStreamInterceptor enforces the shared token on every streaming RPC.
func tokenStreamInterceptor(token string) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := verifyToken(ss.Context(), token); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}
