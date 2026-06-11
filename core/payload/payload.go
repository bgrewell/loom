// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package payload generates the bytes a flow sends. See
// docs/blueprints/payloaders.md and DESIGN.md §5.3.
package payload

// Payloader fills successive buffers with a flow's payload bytes. It follows
// io.Reader semantics but, for an infinite traffic source, never returns
// io.EOF. Implementations should be allocation-free once primed.
type Payloader interface {
	// Name returns the payloader's registry identifier.
	Name() string
	// Read fills p and returns the number of bytes written.
	Read(p []byte) (int, error)
}
