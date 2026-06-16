// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package datapath

// Diag is unavailable off Linux (TCP_INFO is a Linux getsockopt); loom targets
// Linux, this stub only keeps non-Linux builds compiling.
func (s *TCPSocket) Diag() (TCPDiag, bool) { return TCPDiag{}, false }
