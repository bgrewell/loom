// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package datapath

import (
	"encoding/binary"
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

// enableUDPGRO turns on UDP generic receive offload for conn, so the kernel
// coalesces consecutive same-flow datagrams into one recvmsg (with a UDP_GRO
// control message giving the segment size) — one syscall and one stack
// traversal for many datagrams. Best-effort like GSO; false means it is
// unavailable and the caller keeps using plain recvmmsg.
func enableUDPGRO(conn *net.UDPConn) bool {
	sc, err := conn.SyscallConn()
	if err != nil {
		return false
	}
	var setErr error
	if err := sc.Control(func(fd uintptr) {
		setErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_UDP, unix.UDP_GRO, 1)
	}); err != nil {
		return false
	}
	return setErr == nil
}

// groSegSize returns the UDP_GRO segment size carried in a recvmsg's control
// data (the size each coalesced datagram was, except a possibly shorter last
// one), or 0 when the message was not coalesced.
func groSegSize(oob []byte) int {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return 0
	}
	for i := range msgs {
		h := msgs[i].Header
		if h.Level == unix.IPPROTO_UDP && h.Type == unix.UDP_GRO && len(msgs[i].Data) >= 4 {
			return int(nativeUint32(msgs[i].Data))
		}
	}
	return 0
}

// nativeUint32 reads a host-endian int32 from a control-message payload.
func nativeUint32(b []byte) uint32 { return binary.NativeEndian.Uint32(b[:4]) }
