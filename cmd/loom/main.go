// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Command loom is the loom CLI — the primary human interface to the traffic
// generation & measurement system. Built on github.com/bgrewell/stencil
// (ADR-0017).
package main

import (
	"os"

	"github.com/bgrewell/stencil"
)

// Build metadata, injected at link time via -ldflags (see the stencil dev-CLI).
var (
	version   = "dev"
	buildDate = "unknown"
	commit    = "none"
	branch    = "none"
)

func main() {
	app := stencil.NewApp(
		stencil.WithName("loom"),
		stencil.WithDescription("network traffic generation & measurement"),
		stencil.WithVersionInfo(stencil.VersionInfo{
			Version:    version,
			BuildDate:  buildDate,
			CommitHash: commit,
			Branch:     branch,
		}),
	)
	app.Root.Sub = append(app.Root.Sub, runCommand(), serverCommand(), clientCommand())
	os.Exit(app.Execute(os.Args[1:]))
}
