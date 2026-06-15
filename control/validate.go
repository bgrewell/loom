// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"fmt"
	"net"
)

// Agent resource limits. These bound what a single (currently unauthenticated —
// see ADR-0014) Configure call can make the agent allocate or hold, so a hostile
// or buggy controller cannot OOM the process or exhaust file descriptors.
const (
	// minPacketSize is the smallest packet an agent will generate or receive.
	minPacketSize = 1
	// maxPacketSize caps the per-flow buffer allocation. 64 KiB covers the
	// largest UDP datagram with headroom; without it, packet_size is a uint32
	// and one Configure could request a multi-gigabyte allocation.
	maxPacketSize = 64 * 1024
	// defaultMaxFlows caps concurrently configured flows per agent.
	defaultMaxFlows = 1024
)

// validatePacketSize rejects a packet size outside [minPacketSize, maxPacketSize].
func validatePacketSize(n uint32) error {
	if n < minPacketSize || n > maxPacketSize {
		return fmt.Errorf("packet_size %d out of range [%d, %d]", n, minPacketSize, maxPacketSize)
	}
	return nil
}

// validateTransport rejects a request/response transport that is not tcp or udp.
func validateTransport(t string) error {
	if t != "tcp" && t != "udp" {
		return fmt.Errorf("transport %q must be tcp or udp", t)
	}
	return nil
}

// validateTarget checks that a non-empty flow target parses as host:port with a
// valid port. An empty target is allowed (datapaths that need no peer, e.g.
// discard/memory). This is the format-validation seam; an SSRF/reflection
// allowlist (block loopback/link-local/private unless opted in) lands with the
// control-plane auth work (ADR-0014).
func validateTarget(target string) error {
	if target == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("invalid target %q: %w", target, err)
	}
	if host == "" {
		return fmt.Errorf("invalid target %q: empty host", target)
	}
	if p, err := net.LookupPort("udp", port); err != nil || p == 0 {
		return fmt.Errorf("invalid target %q: bad port %q", target, port)
	}
	return nil
}
