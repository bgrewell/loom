// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package netstack is a userspace TCP/IP netpath backend: it wraps gVisor's
// pkg/tcpip (pure Go — no NET_ADMIN, no TUN, no netns) over loom's raw-L3
// datapath frame contract, so TCP-based apps (HTTP/TLS, video) can ride a
// tunneled inner-IP packet lane that the kernel never sees.
//
// ONE Stack hosts MANY local addresses (ADR-0026: never one stack per
// address). An embedder adds and removes addresses as endpoints attach and
// detach (AddAddress/RemoveAddress), and Stack.Network(local) returns a
// source-bound netpath.Network view: DialContext binds the view's local
// address (gonet.DialTCPWithBind), Listen and ListenPacket bind on it, and
// closing a view closes only the conns and listeners created through that
// view — never the Stack. Stack.Close tears everything down deterministically:
// every view's conns, the gVisor stack and its worker goroutines, and the two
// datapaths the Stack owns.
//
// The link layer is dpEndpoint, a stack.LinkEndpoint implemented directly
// over the ADR-0019 frame contract: WritePackets copies each outbound packet
// into TxReserve'd frames and TxCommits the batch (no channel.Endpoint, so no
// second copy per packet), and one receive goroutine loops
// RxPoll → DeliverNetworkPacket → RxRelease, dispatching IPv4/IPv6 by the
// version nibble. It is a pure L3 endpoint — no link addresses, no ARP —
// matching tunneled inner-IP traffic (e.g. the GTP-U payloads orbit carries).
// Both datapaths must advertise datapath.Capabilities.RawL3; New refuses
// backends whose frames are not complete IP packets.
//
// Scope: IPv4 first. AddAddress rejects IPv6 addresses and views reject
// "tcp6"/"udp6" until IPv6 is wired end to end; the endpoint already
// dispatches inbound IPv6 by version nibble, so widening is additive.
//
// gVisor is pinned to v0.0.0-20260717235516-4f55f3833ba5, the tip of its
// module-consumption "go" branch just after release-20260714.0. The
// release-20260714.0 sync itself cannot be imported as a Go module (its
// pkg/tcpip/stack ships a bridge_test.go whose package clause the Go loader
// rejects); the pinned commit is the first go-branch state past that release
// without the defect. gVisor imports are confined to this one package
// (ADR-0026). Building with the loom_nonetstack tag stubs the
// whole package — New returns ErrDisabled — so minimal agents compile without
// the gVisor dependency:
//
//	go build -tags loom_nonetstack ./...
//
// # Phase-6 measurement-hygiene gate (design §9)
//
// Before any TCP-derived number is attributed to the network under test, the
// userspace stack's own contribution is quantified by the harness in
// bench_test.go — run it with:
//
//	go test ./core/netstack -bench . -run TestSenderTimestampAudit -v
//
// Netstack-vs-kernel deltas measured on the development machine (Linux
// 6.17.0, go1.26.3, i7-1165G7, gVisor pinned as below; netstack over the
// in-process memory datapath pair at MTU 1500, kernel TCP over loopback at
// its default ~64 KiB MTU — the deltas bundle stack cost with that framing
// difference, so treat them as the userspace-stack budget, not a precise
// stack-only cost):
//
//	BenchmarkTCPThroughput/netstack     81803 ns/op     400.57 MB/s   (32 KiB writes)
//	BenchmarkTCPThroughput/kernel        5017 ns/op    6531.09 MB/s
//	BenchmarkTCPLatency/netstack        31062 ns/op  (128 B ping-pong RTT)
//	BenchmarkTCPLatency/kernel           6554 ns/op
//
// Sender-side timestamp audit (TestSenderTimestampAudit, same machine): 200
// application writes paced at 2 ms intervals, each write's intended
// departure, actual Write call, and the resulting segment's TxCommit
// timestamped. Write→TxCommit — the stack's own send-side contribution — was
// p50 ≈ 22 µs, p95 ≈ 46 µs, p99 ≈ 60 µs, max ≈ 76 µs; the test's own pacing
// error (intended→Write sleep overshoot, p50 ≈ 0.5 ms) is reported as a
// separate component. Send-side stack delay of tens of microseconds is the
// noise floor to quote next to any TCP-derived timing — report it
// separately; never silently attribute it to the RAN or the network under
// test.
package netstack
