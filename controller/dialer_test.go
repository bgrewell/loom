// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"fmt"
	"io"
	"testing"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
	"github.com/bgrewell/loom/core/scenario"
)

// TestControllerUsesInjectedDialer: WithDialer replaces how the controller
// connects to agents, so it can run without real gRPC (ADR-0022).
func TestControllerUsesInjectedDialer(t *testing.T) {
	var gotAddr string
	d := func(addr string) (loomv1.ControlClient, io.Closer, error) {
		gotAddr = addr
		return nil, nil, fmt.Errorf("boom")
	}
	c := New(&scenario.Scenario{}, map[string]string{"a": "host:1234"}, WithDialer(d))

	if _, _, err := c.agentFor("a"); err == nil || err.Error() != "boom" {
		t.Fatalf("agentFor err = %v, want boom (injected dialer)", err)
	}
	if gotAddr != "host:1234" {
		t.Fatalf("dialer got addr %q, want host:1234", gotAddr)
	}
}
