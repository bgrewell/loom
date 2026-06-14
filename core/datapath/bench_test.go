// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import "testing"

// Datapath throughput benchmarks. Run with:
//
//	go test -bench 'BenchmarkTx' -benchmem ./core/datapath/
//
// b.SetBytes makes `go test -bench` report MB/s per backend, so the numbers can
// be tracked (and diffed with benchstat) over time. They measure the datapath's
// own TX path with a single-frame reserve/commit — matching how the pump drives
// it today — not a real NIC. The AF_XDP/veth number lives in afxdp_bench_test.go.
const benchPkt = 1400

// BenchmarkTxDiscard is the sink baseline: reserve/fill/commit with no I/O.
func BenchmarkTxDiscard(b *testing.B) {
	dp := NewDiscard(benchPkt)
	b.SetBytes(benchPkt)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f := dp.TxReserve(1)
		f[0].Len = benchPkt
		_, _ = dp.TxCommit(f[:1])
	}
}

// BenchmarkTxMemory is the in-process zero-copy loopback (TX then RX from the
// same slab), the no-kernel ceiling for the frame machinery.
func BenchmarkTxMemory(b *testing.B) {
	dp := NewMemory(8, benchPkt)
	b.SetBytes(benchPkt)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f := dp.TxReserve(1)
		f[0].Len = benchPkt
		_, _ = dp.TxCommit(f[:1])
		rf, _ := dp.RxPoll(1)
		dp.RxRelease(rf)
	}
}

// BenchmarkTxUDPSocket measures the kernel-stack UDP send path over loopback,
// with a drained listener on the other end.
func BenchmarkTxUDPSocket(b *testing.B) {
	lis, err := ListenUDP("127.0.0.1:0", benchPkt+64)
	if err != nil {
		b.Fatalf("listen: %v", err)
	}
	go func() { // drain so the kernel receive buffer never wedges the sender
		for {
			frames, err := lis.RxPoll(64)
			if err != nil {
				return
			}
			lis.RxRelease(frames)
		}
	}()

	s, err := DialUDP(lis.conn.LocalAddr().String(), benchPkt)
	if err != nil {
		_ = lis.Close()
		b.Fatalf("dial: %v", err)
	}

	b.SetBytes(benchPkt)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f := s.TxReserve(1)
		f[0].Len = benchPkt
		_, _ = s.TxCommit(f[:1])
	}
	b.StopTimer()
	_ = s.Close()
	_ = lis.Close() // unblocks the drain goroutine (RxPoll errors out)
}
