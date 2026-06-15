// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

//go:build linux && afxdp

package control

import (
	"context"
	"io"
	"net"
	"os"
	"testing"
	"time"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
	"github.com/vishvananda/netlink"
)

// TestAFXDPEndToEnd drives an AF_XDP flow through the full control plane: it
// Configures an afxdp receiver on one veth end and an afxdp sender on the other,
// Starts both, and confirms the receiver accounts bytes via StreamTelemetry.
// This exercises proto -> agent -> RxRegistry/Registry -> afxdp datapath end to
// end. Gated to root + LOOM_AFXDP_TEST; never runs in CI.
func TestAFXDPEndToEnd(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("LOOM_AFXDP_TEST") == "" {
		t.Skip("AF_XDP e2e: set LOOM_AFXDP_TEST=1 and run as root")
	}

	veth := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "loomxdp0"}, PeerName: "loomxdp1"}
	if err := netlink.LinkAdd(veth); err != nil {
		t.Fatalf("create veth: %v", err)
	}
	defer netlink.LinkDel(veth)
	for _, n := range []string{"loomxdp0", "loomxdp1"} {
		l, _ := netlink.LinkByName(n)
		_ = netlink.LinkSetUp(l)
	}

	// One in-process agent hosts both flows; the data path is veth, not loopback.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := NewServer("e2e")
	srv.telemetry = 20 * time.Millisecond
	gs := NewGRPCServer(srv)
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()

	client, conn, err := Dial(lis.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Receiver on loomxdp1 (afxdp loads the redirect program there).
	rxCfg, err := client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role: loomv1.FlowRole_FLOW_ROLE_RECEIVER, Datapath: "afxdp", Iface: "loomxdp1", PacketSize: 1000,
	}})
	if err != nil {
		t.Fatalf("configure receiver: %v", err)
	}
	if _, err := client.Start(ctx, &loomv1.StartRequest{FlowId: rxCfg.GetFlowId()}); err != nil {
		t.Fatalf("start receiver: %v", err)
	}

	// Sender on loomxdp0, blasting raw frames the peer's XDP redirects to the XSK.
	txCfg, err := client.Configure(ctx, &loomv1.ConfigureRequest{Flow: &loomv1.FlowSpec{
		Role: loomv1.FlowRole_FLOW_ROLE_SENDER, Datapath: "afxdp", Iface: "loomxdp0",
		Generator: "stream", Payload: "random", PacketSize: 1000, Count: 200000,
	}})
	if err != nil {
		t.Fatalf("configure sender: %v", err)
	}
	if _, err := client.Start(ctx, &loomv1.StartRequest{FlowId: txCfg.GetFlowId()}); err != nil {
		t.Fatalf("start sender: %v", err)
	}

	// The receiver must account some bytes off the wire.
	stream, err := client.StreamTelemetry(ctx, &loomv1.TelemetryRequest{FlowId: rxCfg.GetFlowId()})
	if err != nil {
		t.Fatalf("stream telemetry: %v", err)
	}
	var maxBytes uint64
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if s.GetBytes() > maxBytes {
			maxBytes = s.GetBytes()
		}
		if maxBytes > 0 {
			break
		}
	}
	if maxBytes == 0 {
		t.Fatal("receiver accounted no bytes over AF_XDP")
	}

	_, _ = client.Destroy(ctx, &loomv1.DestroyRequest{FlowId: txCfg.GetFlowId()})
	_, _ = client.Destroy(ctx, &loomv1.DestroyRequest{FlowId: rxCfg.GetFlowId()})
}
