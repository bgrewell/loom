// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Command loomd is the loom agent — the only component that touches the wire.
// It runs on each node under test, executes flows, and streams telemetry back
// to a controller (DESIGN.md §11). Placeholder pending phase 2 (distributed).
package main

import "fmt"

func main() {
	fmt.Println("loomd (agent) — not yet implemented; see DESIGN.md §11")
}
