// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package netpath

import (
	"context"
	"net"
	"net/netip"
)

// hostNetwork is the kernel-stack Network (the default).
type hostNetwork struct{ local netip.Addr }

// Host returns the kernel-stack Network. When local is a valid, non-unspecified
// address it is bound as the source everywhere: DialContext binds it as the
// dialer's local address (TCP and UDP), and Listen/ListenPacket substitute it
// as the host part when the given address leaves the host empty or unspecified
// (":0", "0.0.0.0:80"). Pass the zero netip.Addr for no binding.
func Host(local netip.Addr) Network { return &hostNetwork{local: local} }

// Name implements Network.
func (*hostNetwork) Name() string { return "host" }

// DialContext implements Network via net.Dialer, binding the network's local
// address when set.
func (h *hostNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	var d net.Dialer
	switch {
	case isStream(network):
		if h.bound() {
			d.LocalAddr = &net.TCPAddr{IP: h.local.AsSlice(), Zone: h.local.Zone()}
		}
	case isPacket(network):
		if h.bound() {
			d.LocalAddr = &net.UDPAddr{IP: h.local.AsSlice(), Zone: h.local.Zone()}
		}
	default:
		return nil, &UnsupportedNetworkError{Impl: "host", Network: network}
	}
	return d.DialContext(ctx, network, address)
}

// ListenPacket implements Network via net.ListenConfig.
func (h *hostNetwork) ListenPacket(network, address string) (net.PacketConn, error) {
	if !isPacket(network) {
		return nil, &UnsupportedNetworkError{Impl: "host", Network: network}
	}
	var lc net.ListenConfig
	return lc.ListenPacket(context.Background(), network, h.bindAddress(address))
}

// Listen implements Network via net.ListenConfig.
func (h *hostNetwork) Listen(network, address string) (net.Listener, error) {
	if !isStream(network) {
		return nil, &UnsupportedNetworkError{Impl: "host", Network: network}
	}
	var lc net.ListenConfig
	return lc.Listen(context.Background(), network, h.bindAddress(address))
}

// Close implements Network. The kernel stack owns nothing to release; conns
// and listeners created through the network outlive it.
func (*hostNetwork) Close() error { return nil }

// bound reports whether the network has a source address to bind.
func (h *hostNetwork) bound() bool { return h.local.IsValid() && !h.local.IsUnspecified() }

// bindAddress substitutes the network's local address as the host part when the
// caller left it empty or unspecified, so a source-bound host network listens
// on the address it dials from. An explicit host is respected; a malformed
// address is passed through for the kernel to report.
func (h *hostNetwork) bindAddress(address string) string {
	if !h.bound() {
		return address
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	if host != "" {
		if ip, perr := netip.ParseAddr(host); perr != nil || !ip.IsUnspecified() {
			return address
		}
	}
	return net.JoinHostPort(h.local.String(), port)
}
