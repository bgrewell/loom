// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"testing"

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
		s := senderSpec(ev, "udp", "10.0.0.1:9000", from)
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
		s := senderSpec(ev, "afxdp", "", from)
		if s.GetDatapath() != "afxdp" || s.GetTarget() != "" {
			t.Errorf("datapath/target = %q/%q", s.GetDatapath(), s.GetTarget())
		}
		if s.GetIface() != "eth9" || s.GetQueue() != 3 {
			t.Errorf("iface/queue = %q/%d", s.GetIface(), s.GetQueue())
		}
	})
}
