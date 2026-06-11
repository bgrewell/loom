// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package datapath is the packet-I/O backend abstraction. Backends range from
// the in-process memory loopback (tests) and kernel sockets (default) to
// AF_PACKET/AF_XDP/DPDK (later phases). See docs/blueprints/datapath-backends.md
// and DESIGN.md §5.1.
package datapath

import "errors"

// Sentinel errors returned by non-blocking backends.
var (
	ErrFull  = errors.New("datapath: send buffer full")
	ErrEmpty = errors.New("datapath: no packet available")
)

// Capabilities describes what a datapath backend supports, so the orchestrator
// can validate a scenario against the available hardware.
type Capabilities struct {
	RawL2              bool   // can send/receive raw layer-2 frames
	HardwareTimestamps bool   // exposes NIC hardware timestamps
	MaxPPS             uint64 // advisory max packets/sec, 0 = unspecified
}

// Datapath sends and receives packets over some backend.
type Datapath interface {
	// Name returns the backend's registry identifier.
	Name() string
	// Caps reports the backend's capabilities.
	Caps() Capabilities
	// Send transmits one packet and returns the bytes written.
	Send(p []byte) (int, error)
	// Recv reads one packet into p and returns the bytes read.
	Recv(p []byte) (int, error)
	// Close releases the backend's resources.
	Close() error
}
