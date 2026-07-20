// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package netstack

import (
	"errors"
	"fmt"

	"github.com/bgrewell/loom/core/components"
	"github.com/bgrewell/loom/core/netpath"
)

// FromOptions is the registry factory: it resolves o.TxDatapath/o.RxDatapath
// by name from c's datapath registries (nil c means components.Default()),
// builds them with o.DatapathOpts, builds a Stack, adds o.Local — the single
// view address for registry-built uses — and returns that address's
// source-bound view. Unlike Stack.Network views, the returned Network owns
// the whole Stack (and the datapaths under it): its Close tears everything
// down, because for a registry-built network the handle is all there is.
//
// Embedders that manage many addresses on one Stack (orbit's per-gNB shape)
// do not use the registry — they call New and Stack.Network directly.
func FromOptions(c *components.Components, o netpath.Options) (netpath.Network, error) {
	c = components.OrDefault(c)
	if o.TxDatapath == "" || o.RxDatapath == "" {
		return nil, errors.New("netstack: Options must name both TxDatapath and RxDatapath")
	}
	if !o.Local.IsValid() {
		return nil, errors.New("netstack: Options.Local must be set (the view's source address)")
	}
	mtu := o.MTU
	if mtu <= 0 {
		mtu = defaultMTU
	}
	dpo := o.DatapathOpts
	if dpo.FrameSize == 0 {
		dpo.FrameSize = mtu
	} else if dpo.FrameSize < mtu {
		// A frame smaller than the MTU cannot carry a full-MSS segment: the
		// endpoint would drop every full-size packet with ErrNoBufferSpace and
		// TCP would retransmit the identical segment forever — a silent stall
		// after a clean handshake. Refuse at build time instead.
		return nil, fmt.Errorf("netstack: DatapathOpts.FrameSize %d is smaller than the MTU %d (frames must fit a full-MTU packet; lower the MTU or raise the frame size)", dpo.FrameSize, mtu)
	}
	tx, err := c.TxDatapaths.Build(o.TxDatapath, dpo)
	if err != nil {
		return nil, fmt.Errorf("netstack: build tx datapath %q: %w", o.TxDatapath, err)
	}
	rx, err := c.RxDatapaths.Build(o.RxDatapath, dpo)
	if err != nil {
		_ = tx.Close()
		return nil, fmt.Errorf("netstack: build rx datapath %q: %w", o.RxDatapath, err)
	}
	s, err := New(Config{MTU: mtu}, tx, rx)
	if err != nil {
		_ = tx.Close()
		_ = rx.Close()
		return nil, err
	}
	if err := s.AddAddress(o.Local); err != nil {
		_ = s.Close()
		return nil, err
	}
	return &ownedNetwork{Network: s.Network(o.Local), s: s}, nil
}

// ownedNetwork is a view that owns its Stack: registry-built networks have no
// separate Stack handle, so Close tears down the view, the Stack, and the
// datapaths.
type ownedNetwork struct {
	netpath.Network
	s *Stack
}

// Close implements netpath.Network.
func (n *ownedNetwork) Close() error {
	errView := n.Network.Close()
	return errors.Join(errView, n.s.Close())
}

func init() {
	// The registry factory is pinned to the DEFAULT component set: Options are
	// pure data (ADR-0006), so a registry entry cannot carry an injected
	// *Components, and datapath names resolve from the global registries.
	// Callers with an injected component set must go through
	// FromOptions(theirComponents, o) — or New with live datapaths — to stay
	// inside their injection boundary (ADR-0022). Registration happens under
	// loom_nonetstack too, so the name resolves and fails with ErrDisabled
	// instead of "unknown name".
	netpath.Registry.Register("netstack", func(o netpath.Options) (netpath.Network, error) {
		return FromOptions(components.Default(), o)
	})
}
