// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import "testing"

func TestRegistryMemory(t *testing.T) {
	d, err := Registry.Build("memory", Options{Size: 4})
	if err != nil || d.Name() != "memory" {
		t.Fatalf("build memory = %v, %v", d, err)
	}
	if _, err := d.Send([]byte("hi")); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := Registry.Build("nope", Options{}); err == nil {
		t.Fatal("unknown datapath should error")
	}
}

func TestRegistryNames(t *testing.T) {
	names := Registry.Names()
	want := map[string]bool{"memory": true, "udp": true, "tcp": true}
	for n := range want {
		found := false
		for _, got := range names {
			if got == n {
				found = true
			}
		}
		if !found {
			t.Fatalf("registry missing %q; have %v", n, names)
		}
	}
}
