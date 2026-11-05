// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package datapath

import "net"

const gsoMaxSegments = 64

// enableUDPSegment is a no-op off Linux: UDP GSO is a Linux socket feature, so
// other platforms always use the sendmmsg path.
func enableUDPSegment(*net.UDPConn, int) bool { return false }

// enableUDPGRO is a no-op off Linux.
func enableUDPGRO(*net.UDPConn) bool { return false }
