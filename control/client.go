// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
)

// Dial connects a control client to addr (host:port). It uses insecure
// transport for now; optional mTLS (ADR-0014) is added later. The returned
// connection must be closed by the caller.
func Dial(addr string) (loomv1.ControlClient, *grpc.ClientConn, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	return loomv1.NewControlClient(conn), conn, nil
}
