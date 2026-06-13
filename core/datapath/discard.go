// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

// Discard is a sink datapath: Send accepts and drops every packet, Recv never
// returns data. It's the "generate but don't deliver" backend used for rate
// tests and benchmarks where no receiver is needed. Allocation-free.
type Discard struct{}

// Name implements Datapath.
func (Discard) Name() string { return "discard" }

// Caps implements Datapath.
func (Discard) Caps() Capabilities { return Capabilities{} }

// Send drops p and reports it fully written.
func (Discard) Send(p []byte) (int, error) { return len(p), nil }

// Recv returns no data.
func (Discard) Recv([]byte) (int, error) { return 0, nil }

// Close is a no-op.
func (Discard) Close() error { return nil }
