// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"

	"github.com/bgrewell/loom/control"
	"github.com/bgrewell/stencil"
)

// serverCommand builds `loom server`: a no-install way to stand up a loom agent so
// a `loom client` elsewhere can run tests against this host. It is the same agent
// loomd runs — if a loomd (or another loom server) is already listening on the
// host/port, you don't need this; just point the client at it.
func serverCommand() *stencil.Command {
	fs := stencil.NewFlagSet()
	fs.Int("port", "p", "control-plane port to listen on", 9551)
	fs.String("bind", "b", "address to bind (default all interfaces)", "0.0.0.0")
	fs.String("token", "", "control-plane auth token (default $LOOM_TOKEN)", "")
	fs.Duration("telemetry-interval", "", "agent sampling cadence (0 = default)", 0)
	return &stencil.Command{
		Name:    "server",
		Summary: "Run a loom agent that accepts client test connections",
		Long:    "Start the loom agent (control plane) on this host so `loom client <host>` can run tests against it. Equivalent to running loomd, with friendlier flags.",
		Flags:   fs,
		Run:     runServer,
	}
}

func runServer(ctx *stencil.Context) error {
	port := ctx.Flags.Int("port")
	bind := ctx.Flags.String("bind")
	token := ctx.Flags.String("token")
	if token == "" {
		token = os.Getenv("LOOM_TOKEN")
	}

	addr := net.JoinHostPort(bind, strconv.Itoa(port))
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	var opts []control.Option
	if token != "" {
		opts = append(opts, control.WithAuthToken(token))
	}
	if d := ctx.Flags.Duration("telemetry-interval"); d > 0 {
		opts = append(opts, control.WithTelemetryInterval(d))
	}
	srv := control.NewServer(version, opts...)
	if !srv.AuthEnabled() && !isLoopback(lis.Addr()) {
		fmt.Fprintf(os.Stderr, "loom server: WARNING: listening on a routable address %s with no token (--token/$LOOM_TOKEN) — the control plane is unauthenticated\n", lis.Addr())
	}
	gs := control.NewGRPCServer(srv)
	fmt.Printf("loom server %s listening on %s\n", version, lis.Addr())

	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		<-c
		gs.GracefulStop()
	}()

	if err := gs.Serve(lis); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// isLoopback reports whether addr is a loopback TCP address, used to decide whether
// an unauthenticated control plane is acceptable. Copied from loomd (which is a
// main package and cannot be imported).
func isLoopback(addr net.Addr) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
