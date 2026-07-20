// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package vidstream

import (
	"io"

	"github.com/bgrewell/loom/core/app"
	"github.com/bgrewell/loom/core/metrics"
)

// Name is the registry name under which the video player registers
// (metrics.KindVideo, and FlowSpec.app on the wire). The app is client-only:
// its server side is the "http" app's HTTPOrigin.
const Name = "video"

func init() {
	app.ClientRegistry.Register(Name, NewClient)
}

// Compile-time checks: the player exposes quality snapshots through the
// metrics.Source seam (plus the whole-run CumulativeMetrics capability the
// agent's final sample prefers) and Close for the built-but-never-run
// teardown path — it owns a connection pool like the httpx client. All
// optional capabilities are discovered by assertion, the flowTCPInfo pattern.
var (
	_ app.Client                                        = (*client)(nil)
	_ metrics.Source                                    = (*client)(nil)
	_ io.Closer                                         = (*client)(nil)
	_ interface{ CumulativeMetrics() metrics.Snapshot } = (*client)(nil)
)
