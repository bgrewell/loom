// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"testing"
	"time"
)

const branchOfficeMix = `
scenario: branch-office-mix
description: Mixed app traffic from branch clients to the edge.
seed: 1337
defaults:
  datapath: socket
  scheduler: { kind: soak }

endpoints:
  - { name: client-a, tags: [vm, 10g, linux] }
  - { name: client-b, tags: [vm, 10g, linux] }
  - { name: edge,     tags: [server, 40g]   }

timeline:
  - name: web
    flow:   { kind: https-browse, object_size: 100KB..3MB, think: 200ms..2s }
    from:   tags(all(10g, linux))
    to:     edge
    start:  0s
    repeat: { interval: 10ms..100ms, jitter: uniform }
    stop:   end-of-test

  - name: admin-ssh
    flow:  { kind: ssh-session }
    from:  client-a
    to:    edge
    start: +45s
    stop:  { after: 1m }

  - name: backup
    flow:     { kind: ftp-transfer }
    from:     client-b
    to:       edge
    datapath: afxdp
    start:    +37s
    stop:     { volume: 123MB }

  - name: voip
    flow:  { kind: voip-call, codec: g711 }
    from:  { any: [client-a, client-b] }
    to:    edge
    start: +20s
    count: 4
    stop:  { after: 30s }
`

func TestParseScenario(t *testing.T) {
	s, err := Parse([]byte(branchOfficeMix))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Name != "branch-office-mix" || s.Seed != 1337 {
		t.Fatalf("name/seed = %q/%d", s.Name, s.Seed)
	}
	if s.Defaults.Datapath != "socket" {
		t.Fatalf("defaults.datapath = %q", s.Defaults.Datapath)
	}
	if len(s.Endpoints) != 3 || len(s.Timeline) != 4 {
		t.Fatalf("endpoints/timeline = %d/%d", len(s.Endpoints), len(s.Timeline))
	}

	byName := map[string]Event{}
	for _, e := range s.Timeline {
		byName[e.Name] = e
	}

	web := byName["web"]
	if web.Flow.Kind != "https-browse" || web.Flow.Params["object_size"] != "100KB..3MB" {
		t.Fatalf("web flow = %+v", web.Flow)
	}
	if web.From.Raw != "tags(all(10g, linux))" || web.To.Raw != "edge" {
		t.Fatalf("web selectors = %+v / %+v", web.From, web.To)
	}
	if web.Repeat == nil || web.Repeat.Interval != "10ms..100ms" {
		t.Fatalf("web repeat = %+v", web.Repeat)
	}
	if !web.Stop.EndOfTest {
		t.Fatalf("web stop = %+v, want end-of-test", web.Stop)
	}

	if got := byName["admin-ssh"].Start.Offset; got != 45*time.Second {
		t.Fatalf("admin-ssh start = %v, want 45s", got)
	}
	if got := byName["admin-ssh"].Stop.After; got != time.Minute {
		t.Fatalf("admin-ssh stop.after = %v, want 1m", got)
	}

	backup := byName["backup"]
	if backup.Datapath != "afxdp" || backup.Stop.Volume != 123*1024*1024 {
		t.Fatalf("backup = datapath %q volume %d", backup.Datapath, backup.Stop.Volume)
	}

	voip := byName["voip"]
	if voip.Count != 4 || voip.From.Mode != "any" || len(voip.From.List) != 2 {
		t.Fatalf("voip = count %d from %+v", voip.Count, voip.From)
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := Parse([]byte("description: no name\ntimeline: [{name: a, flow: {kind: tcp}}]")); err == nil {
		t.Error("missing scenario name should error")
	}
	if _, err := Parse([]byte("scenario: x\ntimeline: []")); err == nil {
		t.Error("empty timeline should error")
	}
	if _, err := Parse([]byte("scenario: x\ntimeline: [{name: a, flow: {kind: tcp}}, {name: a, flow: {kind: udp}}]")); err == nil {
		t.Error("duplicate event name should error")
	}
}

func TestSelectorModes(t *testing.T) {
	// Single mode key: parsed deterministically.
	single := "scenario: x\ntimeline: [{name: a, flow: {kind: udp}, from: {oneOf: [c1, c2]}, to: srv}]"
	sc, err := Parse([]byte(single))
	if err != nil {
		t.Fatalf("single-mode selector: %v", err)
	}
	if got := sc.Timeline[0].From; got.Mode != "oneOf" || len(got.List) != 2 {
		t.Fatalf("from = %+v, want mode=oneOf list=[c1 c2]", got)
	}
	// Multiple mode keys are ambiguous (map order is randomized) and must be
	// rejected rather than silently picking one.
	multi := "scenario: x\ntimeline: [{name: a, flow: {kind: udp}, from: {oneOf: [c1], allOf: [c2]}, to: srv}]"
	if _, err := Parse([]byte(multi)); err == nil {
		t.Error("multi-key selector should error")
	}
}
