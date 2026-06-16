// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package frameaddr

import (
	"net"
	"strings"
	"testing"
)

// /proc/net/arp fixture: one complete entry, one incomplete (flags 0x0), one on
// a different device.
const arpFixture = `IP address       HW type     Flags       HW address            Mask     Device
10.100.0.2       0x1         0x2         02:00:00:00:00:02     *        eth0
10.100.0.3       0x1         0x0         00:00:00:00:00:00     *        eth0
10.100.0.2       0x1         0x2         02:00:00:00:00:99     *        eth1
`

func TestParseARP(t *testing.T) {
	cases := []struct {
		name      string
		iface     string
		ip        string
		wantMAC   string
		wantFound bool
	}{
		{"complete entry", "eth0", "10.100.0.2", "02:00:00:00:00:02", true},
		{"incomplete entry skipped", "eth0", "10.100.0.3", "", false},
		{"wrong device skipped", "eth0", "10.100.0.4", "", false},
		{"matches device", "eth1", "10.100.0.2", "02:00:00:00:00:99", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mac := parseARP(strings.NewReader(arpFixture), tc.iface, net.ParseIP(tc.ip))
			if tc.wantFound {
				if mac == nil || mac.String() != tc.wantMAC {
					t.Fatalf("parseARP = %v, want %s", mac, tc.wantMAC)
				}
			} else if mac != nil {
				t.Fatalf("parseARP = %v, want nil", mac)
			}
		})
	}
}

// TestResolveRejectsBadTarget covers the input validation that doesn't need a NIC.
func TestResolveRejectsBadTarget(t *testing.T) {
	if _, err := Resolve("lo", "not-an-ip:9999"); err == nil {
		t.Error("non-IPv4 target host should error")
	}
	if _, err := Resolve("definitely-no-such-iface", "10.0.0.1:9999"); err == nil {
		t.Error("missing interface should error")
	}
}
