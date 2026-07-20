// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package netstack

import (
	"errors"
	"fmt"
)

const (
	// defaultMTU applies when Config.MTU is unset. Embedders tunneling inner
	// IP (e.g. GTP-U) pass their reduced inner MTU — orbit passes 1400.
	defaultMTU = 1500
	// minMTU is the smallest MTU accepted: the IPv4 minimum-reassembly datagram
	// size, below which a TCP stack has no useful MSS to work with.
	minMTU = 576
	// maxMTU is the IPv4 maximum total packet length.
	maxMTU = 65535
)

// ErrDisabled is returned by New (and everything downstream of it) when loom
// was built with the loom_nonetstack tag, which stubs this package out so
// minimal agents compile without the gVisor dependency. Rebuild without the
// tag to use the userspace TCP stack.
var ErrDisabled = errors.New("netstack: disabled by the loom_nonetstack build tag (gVisor stubbed out)")

// Config configures a Stack.
type Config struct {
	// MTU is the link MTU in bytes — the inner-IP MTU when the datapath is a
	// tunnel payload lane. 0 means 1500; the accepted range is 576..65535.
	MTU int
	// CongestionControl selects the TCP congestion-control algorithm:
	// "cubic" (the default when empty) or "reno". SACK and RACK loss
	// detection are always enabled.
	CongestionControl string
}

// withDefaults returns cfg with defaults applied, or an error when a field is
// out of range.
func (c Config) withDefaults() (Config, error) {
	if c.MTU == 0 {
		c.MTU = defaultMTU
	}
	if c.MTU < minMTU || c.MTU > maxMTU {
		return c, fmt.Errorf("netstack: mtu %d outside the accepted %d..%d range", c.MTU, minMTU, maxMTU)
	}
	switch c.CongestionControl {
	case "":
		c.CongestionControl = "cubic"
	case "cubic", "reno":
	default:
		return c, fmt.Errorf("netstack: unknown congestion control %q (have cubic, reno)", c.CongestionControl)
	}
	return c, nil
}
