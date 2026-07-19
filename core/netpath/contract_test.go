// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package netpath_test

import (
	"net/netip"
	"testing"

	"github.com/bgrewell/loom/core/contract"
	"github.com/bgrewell/loom/core/netpath"
)

func TestNetworkContract(t *testing.T) {
	t.Run("host", func(t *testing.T) {
		h := netpath.Host(netip.MustParseAddr("127.0.0.1"))
		defer h.Close()
		contract.Network(t, h, h, "127.0.0.1:0")
	})
	t.Run("host-unbound", func(t *testing.T) {
		h := netpath.Host(netip.Addr{})
		defer h.Close()
		contract.Network(t, h, h, "127.0.0.1:0")
	})
	t.Run("memory", func(t *testing.T) {
		a, b := netpath.Memory()
		defer a.Close()
		defer b.Close()
		contract.Network(t, a, b, ":0")
	})
}
