// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package voip_test

import (
	"context"
	"fmt"
	"log"
	"net/netip"
	"time"

	"github.com/bgrewell/loom/core/app/voip"
	"github.com/bgrewell/loom/core/netpath"
	"github.com/bgrewell/loom/core/rtp/codec"
)

// ExampleMediaSession runs a bidirectional G.711 call over the kernel stack
// on loopback: an answerer latching onto the first valid source and a caller
// dialing its bound port. Split the two halves across two processes (or two
// hosts) and only the RemoteRTP address changes — this is the same shape the
// Phase 3 `loom rtp --call/--answer` quick mode drives. The example has no
// stable output (timing-dependent live scores), so `go test` compiles it
// without running it; paste it into a main() to watch a real call.
func ExampleMediaSession() {
	pcmu, err := codec.ByName("pcmu")
	if err != nil {
		log.Fatal(err)
	}

	// Answerer: bind an ephemeral even port on the host stack and wait for
	// the first source that passes RTP validity + probation.
	answerer, err := voip.NewMediaSession(netpath.Host(netip.Addr{}), voip.MediaConfig{Codec: pcmu}, nil)
	if err != nil {
		log.Fatal(err)
	}

	// Caller: aim at the answerer's port. On a second host this address is
	// the one the far end advertised (data_port in Phase 3).
	remote := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), answerer.LocalAddr().Port())
	caller, err := voip.NewMediaSession(netpath.Host(netip.Addr{}), voip.MediaConfig{Codec: pcmu, RemoteRTP: remote}, nil)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go answerer.Run(ctx)
	go caller.Run(ctx)

	for i := 0; i < 9; i++ {
		time.Sleep(time.Second)
		v := caller.Metrics()
		fmt.Printf("MOS-CQ %.2f  R %.1f  loss %.1f%%  discard %.1f%%  jitter %.1fms  RTT %.1fms  OWD %.1f±%.1fms (%s)\n",
			v.MOSCQ, v.RFactor, v.LossPct, v.DiscardPct, v.JitterMs, v.RTTMs, v.OWDMs, v.OWDErrMs, v.OWDMethod)
	}
}
