// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package datapath

import (
	"net"

	"golang.org/x/sys/unix"
)

// Diag reads the sender socket's kernel TCP_INFO (getsockopt) for link profiling.
// Returns false when the connection isn't TCP or the syscall fails.
func (s *TCPSocket) Diag() (TCPDiag, bool) {
	tc, ok := s.conn.(*net.TCPConn)
	if !ok {
		return TCPDiag{}, false
	}
	raw, err := tc.SyscallConn()
	if err != nil {
		return TCPDiag{}, false
	}
	var info *unix.TCPInfo
	var gerr error
	if cerr := raw.Control(func(fd uintptr) {
		info, gerr = unix.GetsockoptTCPInfo(int(fd), unix.SOL_TCP, unix.TCP_INFO)
	}); cerr != nil || gerr != nil || info == nil {
		return TCPDiag{}, false
	}
	return TCPDiag{
		TotalRetrans: info.Total_retrans,
		Lost:         info.Lost,
		RttUs:        info.Rtt,
		RttvarUs:     info.Rttvar,
		SndCwnd:      info.Snd_cwnd,
		SndSsthresh:  info.Snd_ssthresh,
	}, true
}
