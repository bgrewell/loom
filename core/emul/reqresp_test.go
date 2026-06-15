// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package emul

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestRequestResponse drives a requester against a responder over both transports
// and checks that the download bytes flow end-to-end and are accounted on both
// sides.
func TestRequestResponse(t *testing.T) {
	for _, transport := range []string{"tcp", "udp"} {
		t.Run(transport, func(t *testing.T) {
			resp, err := ListenResponder(transport, 1400)
			if err != nil {
				t.Fatalf("ListenResponder: %v", err)
			}
			defer resp.Close()

			rctx, rcancel := context.WithCancel(context.Background())
			defer rcancel()
			go func() { _ = resp.Run(rctx) }()

			// One step requesting 1000-byte objects, no think; stop after 10 KB so
			// the run terminates deterministically (10 transactions).
			script := BehaviorScript{{Size: Constant(1000), Think: Constant(0)}}
			target := fmt.Sprintf("127.0.0.1:%d", resp.Port())
			req, err := DialRequester(transport, target, script, 1400, 0, 0, 10_000, 1)
			if err != nil {
				t.Fatalf("DialRequester: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := req.Run(ctx); err != nil {
				t.Fatalf("requester Run: %v", err)
			}

			if got := req.Counters().Bytes(); got < 10_000 {
				t.Errorf("requester received %d bytes, want >= 10000", got)
			}

			// Let the responder finish accounting its last write, then stop it.
			rcancel()
			time.Sleep(50 * time.Millisecond)
			if got := resp.Counters().Bytes(); got < 10_000 {
				t.Errorf("responder served %d bytes, want >= 10000", got)
			}
		})
	}
}

// TestDialRequesterRejectsBadTransport guards the transport allowlist.
func TestDialRequesterRejectsBadTransport(t *testing.T) {
	if _, err := DialRequester("sctp", "127.0.0.1:1", nil, 0, 0, 0, 0, 0); err == nil {
		t.Fatal("DialRequester should reject a non-tcp/udp transport")
	}
}

// TestListenResponderRejectsBadTransport guards the responder transport allowlist.
func TestListenResponderRejectsBadTransport(t *testing.T) {
	if _, err := ListenResponder("sctp", 0); err == nil {
		t.Fatal("ListenResponder should reject a non-tcp/udp transport")
	}
}

// TestRequesterCountBound stops on the transaction count rather than volume.
func TestRequesterCountBound(t *testing.T) {
	resp, err := ListenResponder("tcp", 1400)
	if err != nil {
		t.Fatalf("ListenResponder: %v", err)
	}
	defer resp.Close()
	rctx, rcancel := context.WithCancel(context.Background())
	defer rcancel()
	go func() { _ = resp.Run(rctx) }()

	script := BehaviorScript{{Size: Constant(500), Think: Constant(0)}}
	target := fmt.Sprintf("127.0.0.1:%d", resp.Port())
	req, err := DialRequester("tcp", target, script, 1400, 0, 5, 0, 1) // count=5 transactions
	if err != nil {
		t.Fatalf("DialRequester: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := req.Run(ctx); err != nil {
		t.Fatalf("requester Run: %v", err)
	}
	if got := req.Counters().Packets(); got < 5 {
		t.Errorf("requester completed %d transactions, want >= 5", got)
	}
}
