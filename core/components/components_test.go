// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package components

import (
	"context"
	"net/netip"
	"testing"

	"github.com/bgrewell/loom/core/accounting"
	"github.com/bgrewell/loom/core/app"
	"github.com/bgrewell/loom/core/netpath"
)

func TestDefaultNetworks(t *testing.T) {
	c := Default()
	if c.Networks == nil {
		t.Fatal("Default().Networks is nil")
	}
	n, err := c.Networks.Build("host", netpath.Options{})
	if err != nil {
		t.Fatalf("build host network: %v", err)
	}
	defer n.Close()
	if n.Name() != "host" {
		t.Fatalf("Name = %q, want host", n.Name())
	}
}

// stubApp satisfies app.Client and app.Server for the registry round trip.
type stubApp struct{ acct accounting.Counters }

func (s *stubApp) Name() string                   { return "components-test-stub" }
func (s *stubApp) Run(ctx context.Context) error  { <-ctx.Done(); return nil }
func (s *stubApp) Counters() *accounting.Counters { return &s.acct }
func (s *stubApp) Addr() netip.AddrPort           { return netip.MustParseAddrPort("127.0.0.1:1") }

func TestDefaultAppRegistries(t *testing.T) {
	c := Default()
	if c.AppClients == nil || c.AppServers == nil {
		t.Fatal("Default() app registries are nil")
	}
	// Default() must hand out the package registries apps self-register into
	// (the Networks pattern): a registration through the app package is
	// visible through Components.
	app.ClientRegistry.Register("components-test-stub", func(o app.Options) (app.Client, error) {
		return &stubApp{}, nil
	})
	app.ServerRegistry.Register("components-test-stub", func(o app.Options) (app.Server, error) {
		return &stubApp{}, nil
	})
	cl, err := c.AppClients.Build("components-test-stub", app.Options{})
	if err != nil {
		t.Fatalf("build app client: %v", err)
	}
	if cl.Name() != "components-test-stub" {
		t.Fatalf("client Name = %q", cl.Name())
	}
	srv, err := c.AppServers.Build("components-test-stub", app.Options{})
	if err != nil {
		t.Fatalf("build app server: %v", err)
	}
	if !srv.Addr().IsValid() {
		t.Fatal("server Addr not valid")
	}
	if _, err := c.AppClients.Build("no-such-app", app.Options{}); err == nil {
		t.Fatal("unknown app should error")
	}
}

func TestOrDefault(t *testing.T) {
	if OrDefault(nil) == nil {
		t.Fatal("OrDefault(nil) is nil")
	}
	own := &Components{}
	if OrDefault(own) != own {
		t.Fatal("OrDefault did not pass through an injected Components")
	}
}
