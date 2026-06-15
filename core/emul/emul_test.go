// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package emul

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/datapath"
)

func TestDistSample(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	if got := Constant(160).Sample(r); got != 160 {
		t.Fatalf("constant = %v, want 160", got)
	}
	for i := 0; i < 1000; i++ {
		v := Uniform(10, 20).Sample(r)
		if v < 10 || v > 20 {
			t.Fatalf("uniform out of range: %v", v)
		}
	}
	if Normal(5, 100).Sample(r) < 0 {
		t.Fatal("sample must be clamped non-negative")
	}
}

func TestEmulationsCompile(t *testing.T) {
	for _, name := range Names() {
		if s, err := Build(name, nil); err != nil || len(s) == 0 {
			t.Errorf("%s: Build(nil) = %d steps, %v", name, len(s), err)
		}
	}
	// A couple of param-driven shapes.
	if s, _ := Build("https-browse", Params{"objects": "5"}); len(s) != 5 {
		t.Errorf("https-browse objects=5 => %d steps", len(s))
	}
	if s, _ := Build("ssh-session", Params{"keys": "3", "bulk": "1MB"}); len(s) != 4 {
		t.Errorf("ssh-session keys=3+bulk => %d steps, want 4", len(s))
	}
	if _, err := Build("voip-call", Params{"codec": "bogus"}); err == nil {
		t.Error("voip-call with unknown codec should error")
	}
	if _, err := Build("nope", nil); err == nil {
		t.Error("unknown emulation should error")
	}
}

func TestVoipCodecAndPtime(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	cases := []struct {
		params    Params
		wantSize  float64
		wantThink time.Duration
	}{
		{Params{"codec": "g711"}, 160, 20 * time.Millisecond},                      // 64kbps × 20ms
		{Params{"codec": "g711", "ptime": "30ms"}, 240, 30 * time.Millisecond},     // larger frames, same rate
		{Params{"codec": "g729"}, 20, 20 * time.Millisecond},                       // 8kbps × 20ms
		{Params{"codec": "g711", "frame_size": "200"}, 200, 20 * time.Millisecond}, // override
	}
	for _, c := range cases {
		s, err := Build("voip-call", c.params)
		if err != nil || len(s) != 1 {
			t.Fatalf("%v: Build = %d steps, %v", c.params, len(s), err)
		}
		if got := s[0].Size.Sample(r); got != c.wantSize {
			t.Errorf("%v: frame size = %v, want %v", c.params, got, c.wantSize)
		}
		if got := time.Duration(s[0].Think.Sample(r)); got != c.wantThink {
			t.Errorf("%v: ptime = %v, want %v", c.params, got, c.wantThink)
		}
	}
}

// TestRunnerSendsAndStops drives a CBR-like script over the discard sink and
// confirms it accounts traffic and honors a count stop.
func TestRunnerSendsAndStops(t *testing.T) {
	dp := datapath.NewDiscard(1500)
	// 200-byte objects, no think-time, stop after 50 packets.
	script := BehaviorScript{{Size: Constant(200), Think: Constant(0)}}
	r := NewRunner(script, dp, 1500, 0, 50, 0, 1)

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := r.Counters().Packets(); got < 50 {
		t.Fatalf("packets = %d, want >= 50 (count stop)", got)
	}
}

// TestRunnerThinkRespectsCancel: a long think-time yields promptly to ctx.
func TestRunnerThinkRespectsCancel(t *testing.T) {
	dp := datapath.NewDiscard(1500)
	script := BehaviorScript{{Size: Constant(100), Think: Constant(float64(time.Hour))}}
	r := NewRunner(script, dp, 1500, 0, 0, 0, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel during think-time")
	}
}
