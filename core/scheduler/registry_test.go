// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"testing"
	"time"
)

func TestRegistryBuild(t *testing.T) {
	s, err := Registry.Build("soak", Options{})
	if err != nil || s.Name() != "soak" {
		t.Fatalf("build soak = %v, %v", s, err)
	}
	i, err := Registry.Build("interval", Options{Interval: time.Millisecond})
	if err != nil || i.Name() != "interval" {
		t.Fatalf("build interval = %v, %v", i, err)
	}
	if _, err := Registry.Build("interval", Options{}); err == nil {
		t.Fatal("interval without a positive Interval should error")
	}
	if _, err := Registry.Build("nope", Options{}); err == nil {
		t.Fatal("unknown scheduler should error")
	}
}
