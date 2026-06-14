// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import "net/netip"

// Frame is one packet's storage, owned by the datapath (ADR-0019). On TX the
// caller writes the packet into Data and sets Len; on RX Data[:Len] is the
// received packet and Meta carries its timestamp/source.
//
// A Frame's Data may alias datapath-owned memory (a UMEM frame for AF_XDP, a
// mempool buffer for DPDK), so it is valid only until the matching TxCommit (TX)
// or RxRelease (RX). Callers must not retain Data — or any sub-slice of it — past
// that point; copy out anything that must outlive the frame. This borrow contract
// is what lets a zero-copy backend hand out slices without copying packet bytes.
type Frame struct {
	Data []byte // backing storage; cap is the backend's frame size
	Len  int    // valid bytes: caller-set on TX, datapath-set on RX
	Meta Meta   // RX metadata; zero on TX
}

// Meta is per-packet receive metadata (ADR-0020). Nanos is populated from a
// software clock today and from NIC hardware timestamps once a backend supports
// them, with no signature change. Src is set when the backend exposes the peer.
type Meta struct {
	Nanos int64          // receive timestamp (UnixNano); 0 if unavailable
	Src   netip.AddrPort // source address; zero value if unavailable
}
