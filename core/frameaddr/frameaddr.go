// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package frameaddr resolves the layer-2/3 addressing a raw datapath (AF_XDP)
// needs to emit deliverable frames: the local NIC's MAC and IPv4, and the peer's
// MAC for a target on the same L2 segment. The kernel stack is bypassed on the
// data path, so loom must address frames itself; this is the (Linux, same-subnet)
// resolver that feeds generator.FrameOptions.
package frameaddr

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bgrewell/loom/core/generator"
)

// arpProbes/arpWait bound how long Resolve waits for the neighbor entry after
// nudging the kernel to ARP the target.
const (
	arpProbes = 8
	arpWait   = 150 * time.Millisecond
	srcPort   = 40000 // fixed source UDP port for crafted frames
)

// Resolve returns the frame addressing for sending to target (host:port) out of
// the NIC named iface: src MAC/IP from the interface, dst MAC via the neighbor
// table (the peer must be on the same L2 segment), dst IP/port from target. It
// nudges the kernel to ARP the target if the neighbor entry is missing.
func Resolve(iface, target string) (*generator.FrameOptions, error) {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, fmt.Errorf("frameaddr: interface %q: %w", iface, err)
	}
	if len(ifi.HardwareAddr) != 6 {
		return nil, fmt.Errorf("frameaddr: %q has no usable MAC", iface)
	}
	srcIP, err := firstIPv4(ifi)
	if err != nil {
		return nil, err
	}
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return nil, fmt.Errorf("frameaddr: target %q: %w", target, err)
	}
	dstIP := net.ParseIP(host).To4()
	if dstIP == nil {
		return nil, fmt.Errorf("frameaddr: target host %q is not an IPv4 address", host)
	}
	dstPort, err := strconv.Atoi(portStr)
	if err != nil || dstPort < 0 || dstPort > 65535 {
		return nil, fmt.Errorf("frameaddr: target port %q invalid", portStr)
	}
	dstMAC, err := resolveMAC(iface, dstIP, target)
	if err != nil {
		return nil, err
	}
	return &generator.FrameOptions{
		SrcMAC: ifi.HardwareAddr, DstMAC: dstMAC,
		SrcIP: srcIP, DstIP: dstIP,
		SrcPort: srcPort, DstPort: uint16(dstPort),
	}, nil
}

// firstIPv4 returns the first IPv4 address configured on ifi.
func firstIPv4(ifi *net.Interface) (net.IP, error) {
	addrs, err := ifi.Addrs()
	if err != nil {
		return nil, fmt.Errorf("frameaddr: addrs for %q: %w", ifi.Name, err)
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if v4 := ipn.IP.To4(); v4 != nil {
				return v4, nil
			}
		}
	}
	return nil, fmt.Errorf("frameaddr: %q has no IPv4 address", ifi.Name)
}

// resolveMAC looks up dstIP's MAC in the neighbor table, nudging the kernel to
// ARP it (a throwaway UDP send to target) between retries.
func resolveMAC(iface string, dstIP net.IP, target string) (net.HardwareAddr, error) {
	for i := 0; i < arpProbes; i++ {
		if mac := lookupNeighbor(iface, dstIP); mac != nil {
			return mac, nil
		}
		probe(target)
		time.Sleep(arpWait)
	}
	return nil, fmt.Errorf("frameaddr: could not resolve MAC for %v on %s (same L2 segment required)", dstIP, iface)
}

// probe sends a throwaway datagram so the kernel resolves the destination MAC
// into its neighbor cache. Errors are ignored — it's only a nudge.
func probe(target string) {
	c, err := net.DialTimeout("udp", target, time.Second)
	if err != nil {
		return
	}
	_, _ = c.Write([]byte{0})
	_ = c.Close()
}

// lookupNeighbor reads /proc/net/arp for a complete entry matching ip on iface.
func lookupNeighbor(iface string, ip net.IP) net.HardwareAddr {
	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseARP(f, iface, ip)
}

// parseARP scans /proc/net/arp content for a complete (ATF_COM) entry matching
// ip on iface, returning its MAC. Split out for testability.
func parseARP(r io.Reader, iface string, ip net.IP) net.HardwareAddr {
	want := ip.String()
	sc := bufio.NewScanner(r)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// IP address, HW type, Flags, HW address, Mask, Device
		if len(fields) < 6 || fields[0] != want || fields[5] != iface {
			continue
		}
		flags, _ := strconv.ParseInt(strings.TrimPrefix(fields[2], "0x"), 16, 32)
		if flags&0x2 == 0 { // ATF_COM: entry incomplete
			continue
		}
		if mac, err := net.ParseMAC(fields[3]); err == nil {
			return mac
		}
	}
	return nil
}
