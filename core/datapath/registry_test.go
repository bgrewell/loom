// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import "testing"

func TestRegistryMemory(t *testing.T) {
	d, err := Registry.Build("memory", Options{Size: 4, FrameSize: 64})
	if err != nil || d.Name() != "memory" {
		t.Fatalf("build memory = %v, %v", d, err)
	}
	tx := d.TxReserve(1)
	if len(tx) != 1 {
		t.Fatalf("TxReserve = %d frames", len(tx))
	}
	tx[0].Len = copy(tx[0].Data, []byte("hi"))
	if sent, err := d.TxCommit(tx[:1]); err != nil || sent != 1 {
		t.Fatalf("commit = %d, %v", sent, err)
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
