// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package components

import (
	"testing"

	"github.com/bgrewell/loom/core/netpath"
)

func TestDefaultNetworks(t *testing.T) {
	c := Default()
	if c.Networks == nil {
		t.Fatal("Default().Networks is nil")
	}
	n, err := c.Networks.Build("host", netpath.Options{})
	if err != nil {
		t.Fatalf("build host network: %v", err)
	}
	defer n.Close()
	if n.Name() != "host" {
		t.Fatalf("Name = %q, want host", n.Name())
	}
}

func TestOrDefault(t *testing.T) {
	if OrDefault(nil) == nil {
		t.Fatal("OrDefault(nil) is nil")
	}
	own := &Components{}
	if OrDefault(own) != own {
		t.Fatal("OrDefault did not pass through an injected Components")
	}
}
