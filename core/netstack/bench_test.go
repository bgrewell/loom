// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

//go:build !loom_nonetstack

// Measurement-hygiene harness (design "quantify, don't attribute"): the
// Phase-6 gate for TCP-derived numbers. BenchmarkTCPThroughput and
// BenchmarkTCPLatency publish the netstack-vs-kernel delta over in-process
// paths, and TestSenderTimestampAudit measures the distribution of intended
// departure time vs actual TxCommit time on the send side, so
// userspace-stack scheduling jitter is a known, separately-reported quantity
// — never silently attributed to the network under test. Results are
// recorded in the package documentation (doc.go).
package netstack_test

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/datapath"
	"github.com/bgrewell/loom/core/netstack"
)

// benchPair builds a netstack client/server conn pair over paired memory
// datapaths, with an optional hook observing the client stack's TxCommits.
func benchPair(tb testing.TB, commitHook func([]datapath.Frame)) (client, server net.Conn) {
	tb.Helper()
	mAB, mBA := newL3Mem(), newL3Mem()
	mAB.commitHook = commitHook
	sa, err := netstack.New(netstack.Config{}, mAB, mBA)
	if err != nil {
		tb.Fatalf("New(a): %v", err)
	}
	sb, err := netstack.New(netstack.Config{}, mBA, mAB)
	if err != nil {
		tb.Fatalf("New(b): %v", err)
	}
	tb.Cleanup(func() { sa.Close(); sb.Close() })
	if err := sa.AddAddress(addrA); err != nil {
		tb.Fatal(err)
	}
	if err := sb.AddAddress(addrB); err != nil {
		tb.Fatal(err)
	}
	ln, err := sb.Network(addrB).Listen("tcp", ":7999")
	if err != nil {
		tb.Fatalf("Listen: %v", err)
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	client, err = sa.Network(addrA).DialContext(context.Background(), "tcp", "10.0.0.2:7999")
	if err != nil {
		tb.Fatalf("dial: %v", err)
	}
	select {
	case server = <-accepted:
	case <-time.After(10 * time.Second):
		tb.Fatal("accept timed out")
	}
	tb.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}

// kernelPair builds a kernel-loopback TCP conn pair for the comparison arm.
func kernelPair(tb testing.TB) (client, server net.Conn) {
	tb.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("kernel listen: %v", err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		tb.Fatalf("kernel dial: %v", err)
	}
	select {
	case server = <-accepted:
	case <-time.After(10 * time.Second):
		tb.Fatal("kernel accept timed out")
	}
	tb.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}

// benchThroughput streams b.N × chunk bytes client→server and reports MB/s.
func benchThroughput(b *testing.B, client, server net.Conn) {
	const chunk = 32 << 10
	buf := make([]byte, chunk)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		io.Copy(io.Discard, server)
	}()
	b.SetBytes(chunk)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := client.Write(buf); err != nil {
			b.Fatalf("write: %v", err)
		}
	}
	client.Close()
	wg.Wait()
	b.StopTimer()
}

// benchLatency ping-pongs a small message and reports RTT as ns/op.
func benchLatency(b *testing.B, client, server net.Conn) {
	const size = 128
	go func() { // echo
		buf := make([]byte, size)
		for {
			if _, err := io.ReadFull(server, buf); err != nil {
				return
			}
			if _, err := server.Write(buf); err != nil {
				return
			}
		}
	}()
	msg := make([]byte, size)
	buf := make([]byte, size)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := client.Write(msg); err != nil {
			b.Fatalf("write: %v", err)
		}
		if _, err := io.ReadFull(client, buf); err != nil {
			b.Fatalf("read: %v", err)
		}
	}
	b.StopTimer()
}

// BenchmarkTCPThroughput publishes the netstack-vs-kernel bulk-transfer
// delta: netstack over the paired memory datapath (MTU 1500), kernel over
// loopback. Run both arms and compare — the delta is the userspace-stack
// budget for any throughput number later attributed to a network under test.
func BenchmarkTCPThroughput(b *testing.B) {
	b.Run("netstack", func(b *testing.B) {
		client, server := benchPair(b, nil)
		benchThroughput(b, client, server)
	})
	b.Run("kernel", func(b *testing.B) {
		client, server := kernelPair(b)
		benchThroughput(b, client, server)
	})
}

// BenchmarkTCPLatency publishes the netstack-vs-kernel small-message RTT
// delta (one op = one 128-byte ping-pong).
func BenchmarkTCPLatency(b *testing.B) {
	b.Run("netstack", func(b *testing.B) {
		client, server := benchPair(b, nil)
		benchLatency(b, client, server)
	})
	b.Run("kernel", func(b *testing.B) {
		client, server := kernelPair(b)
		benchLatency(b, client, server)
	})
}

// tcpPayloadLen returns the TCP payload byte count of an IPv4 packet, or -1
// when the packet is not IPv4/TCP.
func tcpPayloadLen(pkt []byte) int {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		return -1
	}
	ihl := int(pkt[0]&0x0F) * 4
	total := int(binary.BigEndian.Uint16(pkt[2:4]))
	if pkt[9] != 6 || total > len(pkt) || ihl+20 > total {
		return -1
	}
	dataOff := int(pkt[ihl+12]>>4) * 4
	return total - ihl - dataOff
}

// TestSenderTimestampAudit is the send-side clock audit: application writes
// are paced on fixed intended departure times, the datapath records each
// data-bearing TxCommit, and the test reports the distribution of
// (TxCommit − intended departure). The spread of that delay — not its
// constant floor — is the scheduling jitter the userspace stack injects
// before packets ever reach the network, which measurement consumers must
// report separately rather than attribute to the path.
func TestSenderTimestampAudit(t *testing.T) {
	const (
		writes   = 200
		interval = 2 * time.Millisecond
		size     = 64
	)
	var mu sync.Mutex
	var commits []time.Time
	hook := func(frames []datapath.Frame) {
		now := time.Now()
		mu.Lock()
		defer mu.Unlock()
		for i := range frames {
			f := &frames[i]
			data := f.Data
			if f.Len < len(data) {
				data = data[:f.Len]
			}
			if tcpPayloadLen(data) > 0 {
				commits = append(commits, now)
			}
		}
	}
	client, server := benchPair(t, hook)

	done := make(chan struct{})
	go func() {
		io.Copy(io.Discard, server)
		close(done)
	}()

	msg := make([]byte, size)
	intended := make([]time.Time, 0, writes)
	wrote := make([]time.Time, 0, writes)
	start := time.Now().Add(10 * time.Millisecond)
	for i := 0; i < writes; i++ {
		due := start.Add(time.Duration(i) * interval)
		time.Sleep(time.Until(due))
		intended = append(intended, due)
		wrote = append(wrote, time.Now())
		if _, err := client.Write(msg); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	client.Close()
	<-done

	mu.Lock()
	got := append([]time.Time(nil), commits...)
	mu.Unlock()
	// Small writes at 2ms spacing normally map 1:1 onto segments. Coalescing
	// or retransmission shifts every later raw-index pairing, silently
	// inflating each reported stack delay by multiples of the pacing interval
	// while still passing the sanity bound — so the audit only scores a
	// perfect 1:1 pairing and skips (never publishes misaligned percentiles)
	// otherwise.
	if len(got) != writes {
		t.Skipf("recorded %d data-bearing TxCommits for %d writes — coalescing/retransmission broke 1:1 pairing; skipping rather than scoring misaligned pairs", len(got), writes)
	}
	n := writes
	// Two separately-reported components: pacing error (the send clock's own
	// sleep overshoot, before the stack is involved) and stack delay (Write
	// call to TxCommit — the userspace stack's contribution).
	pacing := make([]time.Duration, n)
	stackDelay := make([]time.Duration, n)
	total := make([]time.Duration, n)
	for i := 0; i < n; i++ {
		pacing[i] = wrote[i].Sub(intended[i])
		stackDelay[i] = got[i].Sub(wrote[i])
		total[i] = got[i].Sub(intended[i])
		if stackDelay[i] < 0 {
			t.Fatalf("stack delay[%d] = %v < 0: a TxCommit precedes its Write — pairing is misaligned", i, stackDelay[i])
		}
	}
	q := func(ds []time.Duration, p float64) time.Duration {
		s := append([]time.Duration(nil), ds...)
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		return s[int(p*float64(len(s)-1))]
	}
	t.Logf("sender timestamp audit: %d writes, %d data segments paired", writes, len(got))
	t.Logf("pacing error (intended→Write):   p50=%v p95=%v p99=%v max=%v", q(pacing, .50), q(pacing, .95), q(pacing, .99), q(pacing, 1))
	t.Logf("stack delay  (Write→TxCommit):   p50=%v p95=%v p99=%v max=%v", q(stackDelay, .50), q(stackDelay, .95), q(stackDelay, .99), q(stackDelay, 1))
	t.Logf("total        (intended→TxCommit): p50=%v p95=%v p99=%v max=%v", q(total, .50), q(total, .95), q(total, .99), q(total, 1))
	if p95 := q(stackDelay, .95); p95 > 50*time.Millisecond {
		t.Errorf("p95 send-side stack delay %v exceeds the 50ms sanity bound", p95)
	}
}
