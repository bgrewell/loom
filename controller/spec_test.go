// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"testing"
	"time"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
	"github.com/bgrewell/loom/core/scenario"
)

func TestSenderSpecThreadsDatapathAndIface(t *testing.T) {
	ev := scenario.Event{
		Name: "e",
		Flow: scenario.Flow{Kind: "udp", Params: map[string]any{"rate": "10Mbps", "packet_size": 1200}},
		Stop: scenario.Stop{Count: 5},
	}
	from := scenario.Endpoint{Name: "a", Iface: "eth9", Queue: 3}

	t.Run("udp keeps the target", func(t *testing.T) {
		s := senderSpec(ev, "udp", "10.0.0.1:9000", from, 0)
		if s.GetRole() != loomv1.FlowRole_FLOW_ROLE_SENDER {
			t.Errorf("role = %v", s.GetRole())
		}
		if s.GetDatapath() != "udp" || s.GetTarget() != "10.0.0.1:9000" {
			t.Errorf("datapath/target = %q/%q", s.GetDatapath(), s.GetTarget())
		}
		if s.GetIface() != "eth9" || s.GetQueue() != 3 {
			t.Errorf("iface/queue = %q/%d", s.GetIface(), s.GetQueue())
		}
		if s.GetRate() != "10Mbps" || s.GetCount() != 5 || s.GetPacketSize() != 1200 {
			t.Errorf("rate/count/size = %q/%d/%d", s.GetRate(), s.GetCount(), s.GetPacketSize())
		}
	})

	t.Run("afxdp carries iface, no target", func(t *testing.T) {
		s := senderSpec(ev, "afxdp", "", from, 0)
		if s.GetDatapath() != "afxdp" || s.GetTarget() != "" {
			t.Errorf("datapath/target = %q/%q", s.GetDatapath(), s.GetTarget())
		}
		if s.GetIface() != "eth9" || s.GetQueue() != 3 {
			t.Errorf("iface/queue = %q/%d", s.GetIface(), s.GetQueue())
		}
	})

	t.Run("emulation kind is carried with params and seed", func(t *testing.T) {
		eve := scenario.Event{
			Name: "call",
			Flow: scenario.Flow{Kind: "voip-call", Params: map[string]any{"codec": "g711"}},
		}
		s := senderSpec(eve, "udp", "10.0.0.1:9000", from, 42)
		if s.GetEmulation() != "voip-call" {
			t.Errorf("emulation = %q, want voip-call", s.GetEmulation())
		}
		if s.GetParams()["codec"] != "g711" {
			t.Errorf("params = %v, want codec=g711", s.GetParams())
		}
		if s.GetSeed() != 42 {
			t.Errorf("seed = %d, want 42", s.GetSeed())
		}
	})

	t.Run("emulation duration knob maps to the run duration", func(t *testing.T) {
		eve := scenario.Event{
			Name: "call",
			Flow: scenario.Flow{Kind: "voip-call", Params: map[string]any{"duration": "45s"}},
			// no Stop.After set
		}
		s := senderSpec(eve, "udp", "h:1", from, 0)
		if got := s.GetDuration().AsDuration(); got != 45*time.Second {
			t.Errorf("duration = %v, want 45s", got)
		}
	})

	t.Run("explicit stop.after wins over the duration knob", func(t *testing.T) {
		eve := scenario.Event{
			Name: "call",
			Flow: scenario.Flow{Kind: "voip-call", Params: map[string]any{"duration": "45s"}},
			Stop: scenario.Stop{After: 10 * time.Second},
		}
		s := senderSpec(eve, "udp", "h:1", from, 0)
		if got := s.GetDuration().AsDuration(); got != 10*time.Second {
			t.Errorf("duration = %v, want 10s (stop.after wins)", got)
		}
	})

	t.Run("raw kind is not treated as an emulation", func(t *testing.T) {
		eve := scenario.Event{Name: "x", Flow: scenario.Flow{Kind: "udp"}}
		if got := senderSpec(eve, "udp", "h:1", from, 1).GetEmulation(); got != "" {
			t.Errorf("emulation = %q, want empty for raw kind", got)
		}
	})
}

// TestEventDatapath: datapath resolves from the event-level field or, as a
// fallback, from the flow block (where authors naturally put it alongside
// packet_size) — never silently UDP when "tcp" was requested under flow.
func TestEventDatapath(t *testing.T) {
	tests := []struct {
		name string
		ev   scenario.Event
		want string
	}{
		{"event-level wins", scenario.Event{
			Datapath: "afxdp",
			Flow:     scenario.Flow{Params: map[string]any{"datapath": "tcp"}},
		}, "afxdp"},
		{"flow-level fallback", scenario.Event{
			Flow: scenario.Flow{Kind: "tcp", Params: map[string]any{"datapath": "tcp"}},
		}, "tcp"},
		{"default udp", scenario.Event{Flow: scenario.Flow{Kind: "udp"}}, "udp"},
		{"empty flow datapath falls through to udp", scenario.Event{
			Flow: scenario.Flow{Params: map[string]any{"datapath": ""}},
		}, "udp"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := eventDatapath(tc.ev); got != tc.want {
				t.Errorf("eventDatapath = %q, want %q", got, tc.want)
			}
		})
	}
}
