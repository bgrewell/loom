// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/scenario"
)

// TestWaitSourcesAndSnapshot drives a count-bounded sender to completion and
// checks what the loomctl run loop depends on: WaitSources returns once the source
// flow finishes, and Snapshot retains its cumulative bytes for the summary.
func TestWaitSourcesAndSnapshot(t *testing.T) {
	clientAddr, stopClient := startAgent(t)
	defer stopClient()
	serverAddr, stopServer := startAgent(t)
	defer stopServer()

	s := &scenario.Scenario{
		Name: "lifecycle",
		Seed: 1,
		Endpoints: []scenario.Endpoint{
			{Name: "client"},
			{Name: "server"},
		},
		Timeline: []scenario.Event{{
			Name:  "blast",
			Flow:  scenario.Flow{Kind: "udp", Params: map[string]any{"packet_size": 1000}},
			From:  scenario.Selector{Raw: "client"},
			To:    scenario.Selector{Raw: "server"},
			Start: scenario.Start{Offset: 0},
			Stop:  scenario.Stop{Count: 2000},
		}},
	}

	c := New(s, map[string]string{"client": clientAddr, "server": serverAddr})
	defer c.Close()

	tel := NewTelemetry(20 * time.Millisecond)
	defer tel.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go tel.Collect(ctx, c)

	if err := c.Run(ctx, time.Second); err != nil {
		t.Fatalf("controller Run: %v", err)
	}

	// The count-bounded sender must finish on its own, so WaitSources returns true
	// well before the context deadline.
	if !tel.WaitSources(ctx, c) {
		t.Fatal("WaitSources returned false; source flow never completed")
	}

	// The final sample carries the cumulative total, retained by Snapshot.
	time.Sleep(60 * time.Millisecond)
	snap := tel.Snapshot()
	if snap.TxBytes == 0 {
		t.Fatalf("expected non-zero tx bytes after a completed flow, got %+v", snap)
	}
	c.Teardown(context.Background())
}

func TestAggregateSummary(t *testing.T) {
	a := Aggregate{
		TxBytes: 125_000_000, // 1 Gbit over 1s
		RxBytes: 124_000_000,
		Flows: []FlowSample{
			{Event: "blast", Role: Sender, Bytes: 125_000_000},
			{Event: "blast", Role: Receiver, Bytes: 124_000_000},
		},
	}
	out := a.Summary("summ-test", time.Second, true, false)
	if !strings.Contains(out, "--- summary (authoritative) ---") {
		t.Fatalf("missing summary header: %q", out)
	}
	// Header carries scenario, duration, and stream count.
	if !strings.Contains(out, "scenario   summ-test") || !strings.Contains(out, "streams    1") {
		t.Errorf("summary header missing scenario/streams: %q", out)
	}
	if !strings.Contains(out, "125.00 MB") || !strings.Contains(out, "avg 1.00 Gbps") {
		t.Errorf("summary totals/avg wrong: %q", out)
	}
	// per-flow lines present (the event name only appears in per-flow rows).
	if !strings.Contains(out, "blast") {
		t.Errorf("summary missing per-flow rows: %q", out)
	}
	// Without perFlow, no per-row breakdown (event name absent).
	if strings.Contains(a.Summary("summ-test", time.Second, false, false), "blast") {
		t.Error("non-per-flow summary should not list individual flows")
	}
	// The live-incomplete note appears only when flagged.
	if !strings.Contains(a.Summary("summ-test", time.Second, false, true), "reconciled") {
		t.Error("expected reconciled note when liveIncomplete is set")
	}
}

func TestSummaryLossAndTCPDiagnostics(t *testing.T) {
	// UDP with drops: packet-loss line with count + percentage.
	udp := Aggregate{
		TxBytes: 1000, RxBytes: 900, TxPackets: 100, RxPackets: 90,
		Flows: []FlowSample{
			{Event: "blast", Role: Sender, Datapath: "udp", From: "c", To: "s", Bytes: 1000, Packets: 100},
			{Event: "blast", Role: Receiver, Datapath: "udp", From: "c", To: "s", Bytes: 900, Packets: 90},
		},
	}
	out := udp.Summary("u", time.Second, false, false)
	if !strings.Contains(out, "loss") || !strings.Contains(out, "10 of 100 packets") || !strings.Contains(out, "10.00%") {
		t.Errorf("udp loss line wrong: %q", out)
	}
	if strings.Contains(out, "tcp ") {
		t.Errorf("udp summary should not show a tcp line: %q", out)
	}

	// TCP: byte-loss (≈0) plus a tcp diagnostics line from the sender's stats.
	tcp := Aggregate{
		TxBytes: 1000, RxBytes: 1000,
		Flows: []FlowSample{
			{Event: "stream", Role: Sender, Datapath: "tcp", From: "c", To: "s", Bytes: 1000,
				TCP: &TCPStats{Retrans: 12, Lost: 1, RttUs: 420, RttvarUs: 80, Cwnd: 76, Ssthresh: 1 << 31}},
			{Event: "stream", Role: Receiver, Datapath: "tcp", From: "c", To: "s", Bytes: 1000},
		},
	}
	out = tcp.Summary("t", time.Second, false, false)
	if !strings.Contains(out, "tcp ") || !strings.Contains(out, "retrans 12") || !strings.Contains(out, "cwnd 76") {
		t.Errorf("tcp diagnostics line missing/wrong: %q", out)
	}
	if !strings.Contains(out, "ssthresh ∞") { // INT_MAX during slow start renders as ∞
		t.Errorf("large ssthresh should render as ∞: %q", out)
	}
}

func TestTextObserverPerFlow(t *testing.T) {
	a := Aggregate{
		At:           time.Now(),
		TxBitsPerSec: 94e6,
		Flows:        []FlowSample{{Event: "web", Role: Sender, BitsPerSec: 94e6, Bytes: 1_000_000}},
	}
	var buf bytes.Buffer
	NewTextObserver(&buf).WithPerFlow(true).Observe(a)
	out := buf.String()
	if !strings.Contains(out, "web") || !strings.Contains(out, "sender") {
		t.Fatalf("per-flow line missing: %q", out)
	}
	// Off by default.
	var plain bytes.Buffer
	NewTextObserver(&plain).Observe(a)
	if strings.Contains(plain.String(), "sender") {
		t.Errorf("default observer should not print per-flow rows: %q", plain.String())
	}
}
