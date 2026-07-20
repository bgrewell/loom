// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"io"

	"github.com/bgrewell/loom/core/app"
	"github.com/bgrewell/loom/core/metrics"
)

// Name is the registry name under which the HTTP client and origin register
// (metrics.KindHTTP, and FlowSpec.app on the wire).
const Name = "http"

func init() {
	app.ClientRegistry.Register(Name, NewClient)
	app.ServerRegistry.Register(Name, NewServer)
}

// Compile-time checks: both sides expose quality snapshots through the
// metrics.Source seam (plus the whole-run CumulativeMetrics capability the
// agent's final sample prefers), and Close for the built-but-never-run
// teardown path — the origin binds eagerly so Addr is valid at configure
// time, and the client owns a connection pool. All optional capabilities are
// discovered by assertion, the flowTCPInfo pattern.
var (
	_ app.Client                                        = (*client)(nil)
	_ app.Server                                        = (*origin)(nil)
	_ metrics.Source                                    = (*client)(nil)
	_ metrics.Source                                    = (*origin)(nil)
	_ io.Closer                                         = (*client)(nil)
	_ io.Closer                                         = (*origin)(nil)
	_ interface{ CumulativeMetrics() metrics.Snapshot } = (*client)(nil)
	_ interface{ CumulativeMetrics() metrics.Snapshot } = (*origin)(nil)
	_ interface{ CertificatePEM() []byte }              = (*origin)(nil)
)
