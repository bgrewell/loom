// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package datapath

import (
	"net"

	"golang.org/x/sys/unix"
)

// gsoMaxSegments is the kernel's cap on datagrams produced from one UDP GSO
// write (UDP_MAX_SEGMENTS). A GSO buffer must also stay within 65535 bytes.
const gsoMaxSegments = 64

// enableUDPSegment turns on UDP generic segmentation offload for conn, so a
// single write of N*segSize bytes leaves the socket as N datagrams of segSize —
// the kernel walks its stack once for the batch instead of once per datagram.
// It is a best-effort optimisation: many environments disable GSO, so a false
// return (the setsockopt failed) just means the caller keeps using sendmmsg.
func enableUDPSegment(conn *net.UDPConn, segSize int) bool {
	if segSize <= 0 || segSize > 65535 {
		return false
	}
	sc, err := conn.SyscallConn()
	if err != nil {
		return false
	}
	var setErr error
	if err := sc.Control(func(fd uintptr) {
		setErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_UDP, unix.UDP_SEGMENT, segSize)
	}); err != nil {
		return false
	}
	return setErr == nil
}
