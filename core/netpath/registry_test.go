// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package netpath

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestRegistryHost(t *testing.T) {
	n, err := Registry.Build("host", Options{Local: netip.MustParseAddr("127.0.0.1")})
	if err != nil {
		t.Fatalf("build host: %v", err)
	}
	defer n.Close()
	if n.Name() != "host" {
		t.Fatalf("Name = %q, want host", n.Name())
	}
	// Options.Local must reach the built network: an unspecified-host listen
	// binds the configured local address.
	ln, err := n.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	ta, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !ta.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Errorf("listener Addr = %v, want 127.0.0.1:*", ln.Addr())
	}
	if _, err := Registry.Build("nope", Options{}); err == nil {
		t.Fatal("unknown network should error")
	}
}

func TestRegistryMemory(t *testing.T) {
	n, err := Registry.Build("memory", Options{})
	if err != nil {
		t.Fatalf("build memory: %v", err)
	}
	defer n.Close()
	// The registry's memory network is self-connected: a dial through the
	// handle reaches a listener created on the same handle.
	ln, err := n.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	accepted := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.Close()
		}
		accepted <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := n.DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	c.Close()
	if err := <-accepted; err != nil {
		t.Fatalf("Accept: %v", err)
	}
}

func TestRegistryNames(t *testing.T) {
	names := Registry.Names()
	want := map[string]bool{"host": true, "memory": true}
	for n := range want {
		found := false
		for _, got := range names {
			if got == n {
				found = true
			}
		}
		if !found {
			t.Fatalf("registry missing %q; have %v", n, names)
		}
	}
}
