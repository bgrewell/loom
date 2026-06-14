// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
)

// DialOption configures a control-plane dial.
type DialOption func(*dialConfig)

type dialConfig struct{ token string }

// WithToken attaches a shared bearer token (ADR-0014) to every RPC on the
// connection. An empty token is a no-op, so callers can pass it unconditionally.
func WithToken(token string) DialOption {
	return func(c *dialConfig) { c.token = token }
}

// Dial connects a control client to addr (host:port). It uses insecure
// transport for now; optional mTLS (ADR-0014) is added later. Pass WithToken to
// authenticate against an agent that requires a control-plane token. The
// returned connection must be closed by the caller.
func Dial(addr string, opts ...DialOption) (loomv1.ControlClient, *grpc.ClientConn, error) {
	var cfg dialConfig
	for _, o := range opts {
		o(&cfg)
	}
	dialOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if cfg.token != "" {
		dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(tokenCreds{token: cfg.token}))
	}
	conn, err := grpc.NewClient(addr, dialOpts...)
	if err != nil {
		return nil, nil, err
	}
	return loomv1.NewControlClient(conn), conn, nil
}
