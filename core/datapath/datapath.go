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
//
// Deprecated: the single-packet Send/Recv interface is being replaced by the
// batch, zero-copy-capable TxDatapath / RxDatapath (ADR-0019). It remains the
// shape the registry and current backends expose; the SinglePacket adapters
// bridge it to the new interfaces until each backend is migrated.
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

// TxDatapath is the transmit side of a backend (ADR-0019). The caller reserves
// datapath-owned frames, fills them, and commits them — so a zero-copy backend
// (AF_XDP/DPDK) never copies packet bytes and can batch the submit syscall.
type TxDatapath interface {
	// Name returns the backend's registry identifier.
	Name() string
	// Caps reports the backend's capabilities.
	Caps() Capabilities
	// TxReserve returns up to n frames to fill, owned by the datapath and valid
	// only until the next TxReserve or TxCommit. It may return fewer than n (or
	// none) when the backend's ring is full.
	TxReserve(n int) []Frame
	// TxCommit transmits frames[:len(frames)] (each with Len set) and returns the
	// number accepted. It also releases every frame currently reserved (committing
	// a zero-length slice releases them without sending). After TxCommit the
	// frames must not be reused.
	TxCommit(frames []Frame) (sent int, err error)
	// Close releases the backend's resources.
	Close() error
}

// RxDatapath is the receive side of a backend (ADR-0019). Polled frames are
// borrowed from the backend's ring and must be released after use, so a
// zero-copy backend hands out RX-ring descriptors without copying.
type RxDatapath interface {
	// Name returns the backend's registry identifier.
	Name() string
	// Caps reports the backend's capabilities.
	Caps() Capabilities
	// RxPoll returns up to max received frames, borrowed from the datapath and
	// valid only until the next RxPoll or RxRelease. It returns a net.Error with
	// Timeout()==true when no frame arrives within the backend's poll window, so
	// the caller can check for cancellation.
	RxPoll(max int) ([]Frame, error)
	// RxRelease returns previously polled frames to the datapath for reuse.
	RxRelease(frames []Frame)
	// Close releases the backend's resources.
	Close() error
}
