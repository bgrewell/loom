// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// External test package: app cannot import core/flow (cycle through
// core/components), so the Runner-contract assertions live here.
package app_test

import (
	"context"
	"net/netip"
	"testing"

	"github.com/bgrewell/loom/core/accounting"
	"github.com/bgrewell/loom/core/app"
	"github.com/bgrewell/loom/core/flow"
	"github.com/bgrewell/loom/core/netpath"
	"github.com/bgrewell/loom/core/registry"
)

// Client and Server must satisfy the agent's flow.Runner contract so the
// flowManager lifecycle drives apps unchanged (design §2.8). These fail to
// compile if either interface drifts from Runner's method set.
var (
	_ flow.Runner = (app.Client)(nil)
	_ flow.Runner = (app.Server)(nil)
)

// stubApp is a minimal Client+Server: it records the Options it was built
// from, runs until ctx cancellation, and counts one synthetic packet per Run.
type stubApp struct {
	opts app.Options
	addr netip.AddrPort
	acct accounting.Counters
	ran  chan struct{}
}

func (s *stubApp) Name() string                   { return "stub" }
func (s *stubApp) Counters() *accounting.Counters { return &s.acct }
func (s *stubApp) Addr() netip.AddrPort           { return s.addr }
func (s *stubApp) Run(ctx context.Context) error {
	s.acct.Add(1)
	close(s.ran)
	<-ctx.Done()
	return nil
}

func newStub(o app.Options) *stubApp {
	return &stubApp{
		opts: o,
		addr: netip.MustParseAddrPort("127.0.0.1:40000"),
		ran:  make(chan struct{}),
	}
}

func TestRegistryRoundTrip(t *testing.T) {
	// Fresh registries of the same shape as the package-level ones, so the
	// round trip doesn't leave test factories behind in shared state.
	clients := registry.New[app.Client, app.Options]()
	servers := registry.New[app.Server, app.Options]()
	var builtWith app.Options
	clients.Register("stub", func(o app.Options) (app.Client, error) {
		builtWith = o
		return newStub(o), nil
	})
	servers.Register("stub", func(o app.Options) (app.Server, error) {
		return newStub(o), nil
	})

	// Options carry live values (Network, here from the memory fabric) plus
	// data; both must reach the factory intact.
	na, nb := netpath.Memory()
	defer na.Close()
	defer nb.Close()
	opts := app.Options{
		Params:  map[string]string{"codec": "pcmu"},
		Seed:    42,
		MTU:     1400,
		Network: na,
		Target:  "peer:5004",
	}

	c, err := clients.Build("stub", opts)
	if err != nil {
		t.Fatalf("Build client: %v", err)
	}
	if c.Name() != "stub" {
		t.Errorf("client Name = %q, want stub", c.Name())
	}
	if builtWith.Network != na || builtWith.Target != "peer:5004" ||
		builtWith.Seed != 42 || builtWith.MTU != 1400 ||
		builtWith.Params["codec"] != "pcmu" {
		t.Errorf("factory received %+v, want the options passed to Build", builtWith)
	}

	srv, err := servers.Build("stub", opts)
	if err != nil {
		t.Fatalf("Build server: %v", err)
	}
	if !srv.Addr().IsValid() || srv.Addr().Port() != 40000 {
		t.Errorf("server Addr = %v, want 127.0.0.1:40000", srv.Addr())
	}

	if _, err := clients.Build("nope", app.Options{}); err == nil {
		t.Fatal("unknown app client should error")
	}
	if _, err := servers.Build("nope", app.Options{}); err == nil {
		t.Fatal("unknown app server should error")
	}
}

// TestStubRunnerLifecycle drives a stub through the Runner contract the way
// the agent's flowManager does: run in a goroutine, observe counters live,
// cancel, and require a clean return.
func TestStubRunnerLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name string
		mk   func(s *stubApp) flow.Runner
	}{
		{"client", func(s *stubApp) flow.Runner { var c app.Client = s; return c }},
		{"server", func(s *stubApp) flow.Runner { var sv app.Server = s; return sv }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := newStub(app.Options{})
			r := tc.mk(stub)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- r.Run(ctx) }()
			<-stub.ran
			if got := r.Counters().Packets(); got != 1 {
				t.Errorf("Counters().Packets() = %d, want 1", got)
			}
			cancel()
			if err := <-done; err != nil {
				t.Errorf("Run returned %v, want nil on cancellation", err)
			}
		})
	}
}
