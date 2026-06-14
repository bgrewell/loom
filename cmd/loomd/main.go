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
)

var version = "dev"

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

	srv := control.NewServer(version)
	if v := os.Getenv("LOOMD_TELEMETRY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			srv.SetTelemetryInterval(d)
		}
	}
	if v := os.Getenv("LOOMD_TOKEN"); v != "" {
		srv.SetAuthToken(v)
	}
	if !srv.AuthEnabled() && !isLoopback(lis.Addr()) {
		fmt.Fprintf(os.Stderr, "loomd: WARNING: listening on a routable address %s with no LOOMD_TOKEN — the control plane is unauthenticated\n", lis.Addr())
	}
	gs := control.NewGRPCServer(srv)
	fmt.Printf("loomd control plane listening on %s\n", lis.Addr())

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
