// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package datapath

import (
	"net"
	"testing"
	"time"
)

// TestUDPSocketGSOSendsSeparateDatagrams verifies the GSO path produces the same
// observable result as per-datagram sends: a batch leaves the wire as N distinct,
// correctly sized datagrams. Skips where the environment has UDP GSO disabled.
func TestUDPSocketGSOSendsSeparateDatagrams(t *testing.T) {
	rx, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer rx.Close()

	const size = 1200
	sock, err := DialUDP(rx.LocalAddr().String(), size)
	if err != nil {
		t.Fatal(err)
	}
	defer sock.Close()
	if !sock.gso {
		t.Skip("UDP GSO not available here")
	}

	const n = 10
	frames := sock.TxReserve(n)
	if len(frames) < n {
		t.Fatalf("reserved %d frames, want %d", len(frames), n)
	}
	for i := 0; i < n; i++ {
		for j := 0; j < size; j++ {
			frames[i].Data[j] = byte(i)
		}
		frames[i].Len = size
	}
	sent, err := sock.TxCommit(frames[:n])
	if err != nil {
		t.Fatalf("TxCommit: %v", err)
	}
	if sent != n {
		t.Fatalf("sent %d datagrams, want %d", sent, n)
	}

	_ = rx.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	got := 0
	for got < n {
		m, _, rerr := rx.ReadFromUDP(buf)
		if rerr != nil {
			break
		}
		if m != size {
			t.Fatalf("datagram %d length %d, want %d (GSO mis-segmented)", got, m, size)
		}
		if buf[0] != byte(got) {
			t.Fatalf("datagram %d first byte %d, want %d (out of order)", got, buf[0], got)
		}
		got++
	}
	if got != n {
		t.Fatalf("received %d datagrams, want %d", got, n)
	}
}
