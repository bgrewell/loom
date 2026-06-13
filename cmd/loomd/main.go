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

	"github.com/bgrewell/loom/control"
)

var version = "dev"

func main() {
	addr := ":9551"
	if v := os.Getenv("LOOMD_ADDR"); v != "" {
		addr = v
	}

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "loomd: listen %s: %v\n", addr, err)
		os.Exit(1)
	}

	gs := control.NewGRPCServer(control.NewServer(version))
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
