// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package emul

import (
	"fmt"
	"strconv"

	"github.com/bgrewell/loom/core/registry"
)

// Params are an emulation's tuning knobs, taken verbatim from a scenario event's
// flow params (stringified). Each emulation documents the keys it honors.
type Params map[string]string

// Registry holds the emulation compilers by name. An emulation is a factory that
// turns Params into a BehaviorScript; the agent looks one up by the scenario's
// flow kind.
var Registry = registry.New[BehaviorScript, Params]()

// Names returns the registered emulation names.
func Names() []string { return Registry.Names() }

// Has reports whether name is a registered emulation.
func Has(name string) bool {
	for _, n := range Registry.Names() {
		if n == name {
			return true
		}
	}
	return false
}

// Build compiles the named emulation from params.
func Build(name string, p Params) (BehaviorScript, error) {
	return Registry.Build(name, p)
}

// --- small param helpers ---

func str(p Params, key, def string) string {
	if v, ok := p[key]; ok && v != "" {
		return v
	}
	return def
}

func intParam(p Params, key string, def int) (int, error) {
	v, ok := p[key]
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("param %q: %w", key, err)
	}
	return n, nil
}

func sizeParam(p Params, key, def string) (Dist, error) {
	d, err := SizeDist(str(p, key, def))
	if err != nil {
		return Dist{}, fmt.Errorf("param %q: %w", key, err)
	}
	return d, nil
}

func durParam(p Params, key, def string) (Dist, error) {
	d, err := DurationDist(str(p, key, def))
	if err != nil {
		return Dist{}, fmt.Errorf("param %q: %w", key, err)
	}
	return d, nil
}
