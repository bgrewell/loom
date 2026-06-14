// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

//go:build linux && afxdp

package datapath

import (
	"os"
	"testing"

	"github.com/vishvananda/netlink"
)

// BenchmarkTxAFXDPVeth measures AF_XDP transmit throughput over a throwaway veth
// pair. Gated like the AF_XDP tests (root + LOOM_AFXDP_TEST). Run with:
//
//	sudo LOOM_AFXDP_TEST=1 go test -tags afxdp -bench AFXDP -benchtime 2s ./core/datapath/
//
// It batches sends (afxdpBenchBatch), so it reports the datapath's capability,
// not the per-packet-syscall rate the pump's one-frame loop currently imposes.
// veth is a kernel software pipe, not a NIC, so this is the test-env ceiling.
const afxdpBenchBatch = 64

func BenchmarkTxAFXDPVeth(b *testing.B) {
	if os.Geteuid() != 0 || os.Getenv("LOOM_AFXDP_TEST") == "" {
		b.Skip("AF_XDP bench: set LOOM_AFXDP_TEST=1 and run as root")
	}
	veth := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "loomxdp0"}, PeerName: "loomxdp1"}
	if err := netlink.LinkAdd(veth); err != nil {
		b.Fatalf("create veth: %v", err)
	}
	defer netlink.LinkDel(veth)
	for _, n := range []string{"loomxdp0", "loomxdp1"} {
		l, _ := netlink.LinkByName(n)
		_ = netlink.LinkSetUp(l)
	}

	tx, err := NewAFXDPTx("loomxdp0", 0)
	if err != nil {
		b.Fatalf("NewAFXDPTx: %v", err)
	}
	defer tx.Close()

	b.SetBytes(benchPkt)
	b.ReportAllocs()
	b.ResetTimer()
	for done := 0; done < b.N; {
		n := afxdpBenchBatch
		if done+n > b.N {
			n = b.N - done
		}
		frames := tx.TxReserve(n)
		if len(frames) == 0 {
			_, _, _ = tx.xsk.Poll(0) // reap completions to free frames
			continue
		}
		for i := range frames {
			frames[i].Len = benchPkt
		}
		sent, err := tx.TxCommit(frames)
		if err != nil {
			b.Fatalf("TxCommit: %v", err)
		}
		done += sent
	}
}
