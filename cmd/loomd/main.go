// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Command loomd is the loom agent — the only component that touches the wire. It
// serves the control plane (DESIGN.md §11/§8); flow execution and telemetry
// streaming are filled in by the Phase-2 agent work.
package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"time"

	"github.com/bgrewell/loom/control"

	// Register the built-in app engines (self-registration into the app
	// registries, ADR-0022's Default() set): a stock loomd advertises them via
	// Capabilities.apps and serves them under the APP_CLIENT/APP_SERVER roles.
	_ "github.com/bgrewell/loom/core/app/httpx"
	_ "github.com/bgrewell/loom/core/app/vidstream"
	_ "github.com/bgrewell/loom/core/app/voip"

	// Register the "netstack" netpath network so Capabilities.networks
	// advertises it and FlowSpec.network="netstack" resolves. This is the
	// import the loom_nonetstack build tag exists for: without the tag loomd
	// carries gVisor; with it the same name resolves and fails with
	// ErrDisabled instead of "unknown network" (design §2.12 skew gate).
	_ "github.com/bgrewell/loom/core/netstack"
)

// Build metadata, injected at link time via -ldflags (see .goreleaser.yaml).
// loomd isn't a stencil CLI, but it reports the same build info on startup and
// over the control plane (Health.version).
var (
	version   = "dev"
	buildDate = "unknown"
	commit    = "none"
	branch    = "none"
)

func main() {
	// Default to loopback: an agent is a remotely-aimable traffic generator, so
	// it must not be reachable off-host unless the operator opts in via
	// LOOMD_ADDR (and, on a routable address, LOOMD_TOKEN).
	addr := "127.0.0.1:9551"
	if v := os.Getenv("LOOMD_ADDR"); v != "" {
		addr = v
	}

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "loomd: listen %s: %v\n", addr, err)
		os.Exit(1)
	}

	var opts []control.Option
	if v := os.Getenv("LOOMD_TELEMETRY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			opts = append(opts, control.WithTelemetryInterval(d))
		}
	}
	if v := os.Getenv("LOOMD_TOKEN"); v != "" {
		opts = append(opts, control.WithAuthToken(v))
	}
	srv := control.NewServer(version, opts...)
	if !srv.AuthEnabled() && !isLoopback(lis.Addr()) {
		fmt.Fprintf(os.Stderr, "loomd: WARNING: listening on a routable address %s with no LOOMD_TOKEN — the control plane is unauthenticated\n", lis.Addr())
	}
	gs := control.NewGRPCServer(srv)
	fmt.Printf("loomd %s (%s, %s, built %s) control plane listening on %s\n",
		version, commit, branch, buildDate, lis.Addr())

	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		<-c
		gs.GracefulStop()
	}()

	if err := gs.Serve(lis); err != nil {
		fmt.Fprintf(os.Stderr, "loomd: serve: %v\n", err)
		os.Exit(1)
	}
}

// isLoopback reports whether addr is a loopback (or unspecified-but-host-local)
// TCP address, used to decide whether an unauthenticated plane is acceptable.
func isLoopback(addr net.Addr) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
