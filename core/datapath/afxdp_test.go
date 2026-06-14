// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

//go:build linux && afxdp

package datapath

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
)

// TestAFXDPVethLoopback validates the AF_XDP TX/RX path over a throwaway veth
// pair: it transmits a raw Ethernet frame on one end and receives the same bytes
// on the other, zero-copy. It is heavily gated — it needs root (AF_XDP sockets +
// eBPF) and an explicit opt-in, so it never runs in normal CI. Run it on the
// testbed (or a dev host) with:
//
//	sudo LOOM_AFXDP_TEST=1 go test -tags afxdp -run AFXDP ./core/datapath/
//
// It creates and deletes interfaces named loomxdp0/loomxdp1.
func TestAFXDPVethLoopback(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("LOOM_AFXDP_TEST") == "" {
		t.Skip("AF_XDP test: set LOOM_AFXDP_TEST=1 and run as root")
	}

	const (
		txName = "loomxdp0"
		rxName = "loomxdp1"
	)
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: txName},
		PeerName:  rxName,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		t.Fatalf("create veth: %v", err)
	}
	defer netlink.LinkDel(veth)

	for _, name := range []string{txName, rxName} {
		link, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatalf("link %s: %v", name, err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatalf("set %s up: %v", name, err)
		}
	}

	rx, err := NewAFXDPRx(rxName, 0)
	if err != nil {
		t.Fatalf("NewAFXDPRx: %v", err)
	}
	defer rx.Close()

	tx, err := NewAFXDPTx(txName, 0)
	if err != nil {
		t.Fatalf("NewAFXDPTx: %v", err)
	}
	defer tx.Close()

	// A minimal Ethernet frame: broadcast dst, zero src, IPv4 ethertype, payload.
	frame := make([]byte, 64)
	for i := 0; i < 6; i++ {
		frame[i] = 0xff
	}
	frame[12], frame[13] = 0x08, 0x00
	copy(frame[14:], []byte("loom-afxdp-zero-copy"))

	tf := tx.TxReserve(1)
	if len(tf) != 1 {
		t.Fatalf("TxReserve returned %d frames", len(tf))
	}
	n := copy(tf[0].Data, frame)
	tf[0].Len = n
	if sent, err := tx.TxCommit(tf[:1]); err != nil || sent != 1 {
		t.Fatalf("TxCommit = %d, %v", sent, err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		frames, err := rx.RxPoll(8)
		if err != nil {
			t.Fatalf("RxPoll: %v", err)
		}
		for i := range frames {
			if bytes.Contains(frames[i].Data[:frames[i].Len], []byte("loom-afxdp-zero-copy")) {
				rx.RxRelease(frames)
				return // success
			}
		}
		rx.RxRelease(frames)
	}
	t.Fatal("did not receive the transmitted frame within the deadline")
}
