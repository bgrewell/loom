// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

//go:build loom_nonetstack

package netstack_test

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/netpath"
	"github.com/bgrewell/loom/core/netstack"
)

// TestStubReturnsErrDisabled pins the loom_nonetstack contract: the package
// compiles without gVisor, New refuses with the documented sentinel, and the
// registry name still resolves (to the same refusal, not "unknown name").
func TestStubReturnsErrDisabled(t *testing.T) {
	m := datapath.NewMemory(8, 512)
	if _, err := netstack.New(netstack.Config{}, m, m); !errors.Is(err, netstack.ErrDisabled) {
		t.Errorf("New = %v, want ErrDisabled", err)
	}
	// Local is set and both datapath names resolve ("memory" tx, "udp" rx —
	// the same pair the non-stub TestRegistryFactory builds successfully), so
	// FromOptions gets past its own validation and the error really is the
	// stub New's ErrDisabled — otherwise this would pass for the wrong reason
	// (an Options validation error) and never pin the contract.
	if _, err := netpath.Registry.Build("netstack", netpath.Options{
		Local:      netip.MustParseAddr("10.0.0.1"),
		TxDatapath: "memory",
		RxDatapath: "udp",
	}); !errors.Is(err, netstack.ErrDisabled) {
		t.Errorf("Registry.Build(netstack) under loom_nonetstack = %v, want ErrDisabled", err)
	}
	var s *netstack.Stack
	if err := s.AddAddress(netip.MustParseAddr("10.0.0.1")); !errors.Is(err, netstack.ErrDisabled) {
		t.Errorf("AddAddress = %v, want ErrDisabled", err)
	}
}
