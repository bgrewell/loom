// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package payload

import "testing"

func TestRegistryBuild(t *testing.T) {
	r, err := Registry.Build("random", Options{Size: 64, Seed: 7})
	if err != nil || r.Name() != "random" {
		t.Fatalf("build random = %v, %v", r, err)
	}
	p, err := Registry.Build("patterned", Options{})
	if err != nil || p.Name() != "patterned" {
		t.Fatalf("build patterned = %v, %v", p, err)
	}
	if _, err := Registry.Build("nope", Options{}); err == nil {
		t.Fatal("unknown payload should error")
	}
}

func TestRegistryRandomDefaultSize(t *testing.T) {
	r, err := Registry.Build("random", Options{}) // Size 0 → default
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := r.Read(make([]byte, 16)); n != 16 {
		t.Fatalf("read = %d, want 16", n)
	}
}
