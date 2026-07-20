// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bgrewell/stencil"
)

// syncBuf is a writer safe to read while the command goroutine writes it.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// rtpApp builds a loom CLI wired like main() but with captured IO, so tests
// drive the real command entrypoint — flag parsing, validation, run loop, and
// exit code — in process.
func rtpApp(out *syncBuf) *stencil.App {
	app := stencil.NewApp(
		stencil.WithName("loom"),
		stencil.WithIO(strings.NewReader(""), out, out),
	)
	app.Root.Sub = append(app.Root.Sub, runCommand(), rtpCommand())
	return app
}

// exec runs one argv through a fresh app and returns exit code and output.
func exec(t *testing.T, argv ...string) (int, string) {
	t.Helper()
	out := &syncBuf{}
	code := rtpApp(out).Execute(argv)
	return code, out.String()
}

// TestRTPFlagValidation exercises the flag surface: contradictory modes and
// port selections must fail with a nonzero exit and a message naming the
// problem, before any socket is touched.
func TestRTPFlagValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		argv []string
		want string // substring of the error output
	}{
		{"neither mode", []string{"rtp"}, "exactly one of --answer or --call"},
		{"both modes", []string{"rtp", "--answer", "--call", "h:1"}, "exactly one of --answer or --call"},
		{"bad codec", []string{"rtp", "--answer", "--codec", "bogus"}, "unknown codec"},
		{"port on caller", []string{"rtp", "--call", "h:1", "--port", "4000"}, "apply to --answer"},
		{"port vs range", []string{"rtp", "--answer", "--port", "4000", "--port-min", "1", "--port-max", "2"}, "--port conflicts"},
		{"half range", []string{"rtp", "--answer", "--port-min", "4000"}, "go together"},
		{"inverted range", []string{"rtp", "--answer", "--port-min", "4002", "--port-max", "4000"}, "empty port range"},
		{"bad target", []string{"rtp", "--call", "no-port"}, "host:port"},
		{"zero interval", []string{"rtp", "--answer", "--interval", "0s"}, "--interval must be positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, out := exec(t, tc.argv...)
			if code == 0 {
				t.Fatalf("exit code 0, want nonzero; output:\n%s", out)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("output missing %q:\n%s", tc.want, out)
			}
		})
	}
}

// TestRTPCodecAliases pins the CLI's friendly codec spellings onto the codec
// table's registered names.
func TestRTPCodecAliases(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"g711": "pcmu", "G711": "pcmu", "g711u": "pcmu",
		"g711a": "pcma", "pcmu": "pcmu", "OPUS": "opus",
	} {
		if got := codecNameFor(in); got != want {
			t.Errorf("codecNameFor(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRTPLoopbackCall runs the real two-command flow in process: an answerer
// on an ephemeral loopback port (human output) and a caller against its
// printed address (json output). Both must exit 0, the answerer must print
// interval lines and a summary, and the caller must emit parseable start/
// interval/summary events.
func TestRTPLoopbackCall(t *testing.T) {
	ansOut := &syncBuf{}
	ansDone := make(chan int, 1)
	go func() {
		ansDone <- rtpApp(ansOut).Execute([]string{
			"rtp", "--answer", "--duration", "2500ms", "--interval", "200ms",
		})
	}()

	// The --call argument is the answerer's printed address.
	addrRe := regexp.MustCompile(`answering on (\S+)`)
	var addr string
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if m := addrRe.FindStringSubmatch(ansOut.String()); m != nil {
			addr = m[1]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if addr == "" {
		t.Fatalf("answerer never printed its bound address:\n%s", ansOut.String())
	}
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		t.Fatalf("unparseable bound address %q: %v", addr, err)
	}
	port := strconv.Itoa(int(ap.Port()))

	code, callOut := exec(t,
		"rtp", "--call", "127.0.0.1:"+port, "--codec", "g711",
		"--duration", "1s", "--interval", "200ms", "--json",
	)
	if code != 0 {
		t.Fatalf("caller exit %d, want 0; output:\n%s", code, callOut)
	}

	// Caller: json-lines events — a start, interval lines for the local end,
	// and a summary carrying the cumulative metrics.VoIP snapshot.
	var types []string
	intervals := 0
	for _, line := range strings.Split(strings.TrimSpace(callOut), "\n") {
		var ev struct {
			Type string `json:"type"`
			End  string `json:"end"`
			VoIP *struct {
				Codec     string  `json:"codec"`
				TxPackets uint64  `json:"tx_packets"`
				MOSCQ     float64 `json:"mos_cq"`
				OWDMethod string  `json:"owd_method"`
			} `json:"voip"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("non-JSON line %q: %v", line, err)
		}
		types = append(types, ev.Type)
		if ev.Type == "interval" && ev.End == "local" {
			intervals++
			if ev.VoIP == nil || ev.VoIP.Codec != "pcmu" {
				t.Fatalf("interval event without pcmu voip snapshot: %s", line)
			}
		}
		if ev.Type == "summary" && ev.End == "local" {
			if ev.VoIP == nil || ev.VoIP.TxPackets == 0 {
				t.Fatalf("summary without cumulative tx packets: %s", line)
			}
		}
	}
	if types[0] != "start" {
		t.Fatalf("first caller event %q, want start", types[0])
	}
	if intervals < 2 {
		t.Fatalf("caller printed %d local interval events, want >= 2:\n%s", intervals, callOut)
	}
	if !strings.Contains(callOut, `"type":"summary"`) {
		t.Fatalf("caller printed no summary:\n%s", callOut)
	}

	if code := <-ansDone; code != 0 {
		t.Fatalf("answerer exit %d, want 0; output:\n%s", code, ansOut.String())
	}
	ans := ansOut.String()
	if n := strings.Count(ans, "] rx "); n < 2 {
		t.Fatalf("answerer printed %d interval lines, want >= 2:\n%s", n, ans)
	}
	if !strings.Contains(ans, "--- summary ---") {
		t.Fatalf("answerer printed no summary:\n%s", ans)
	}
}

// TestRTPHandshakeFailure points a short-fused caller at a dead port: the
// typed handshake error must surface as a nonzero exit with a message that
// says what to check, and no summary block.
func TestRTPHandshakeFailure(t *testing.T) {
	t.Parallel()
	code, out := exec(t,
		"rtp", "--call", "127.0.0.1:9", "--handshake-timeout", "300ms",
		"--duration", "2s", "--interval", "100ms",
	)
	if code == 0 {
		t.Fatalf("exit code 0, want nonzero; output:\n%s", out)
	}
	if !strings.Contains(out, "handshake") || !strings.Contains(out, "loom rtp --answer") {
		t.Fatalf("handshake failure message unclear:\n%s", out)
	}
	if strings.Contains(out, "--- summary ---") {
		t.Fatalf("summary printed after failed handshake:\n%s", out)
	}
}
