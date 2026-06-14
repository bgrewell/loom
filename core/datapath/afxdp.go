// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

//go:build linux && afxdp

// Package datapath's AF_XDP backend is a kernel-bypass, zero-copy datapath
// (DESIGN.md §5.1, ADR-0008). It is built only with the `afxdp` tag because it
// pulls in eBPF/netlink dependencies, needs a real NIC (or veth) plus root
// (CAP_NET_RAW + CAP_BPF), and so is validated on the physical/veth testbed, not
// the default CI. Frames alias the UMEM directly, so it satisfies the zero-copy
// borrow contract of TxDatapath/RxDatapath with no packet copy.
//
// AF_XDP operates at layer 2: a TX frame is a complete Ethernet frame and an RX
// frame is whatever the NIC delivered, so this backend advertises RawL2. Turning
// a loom flow into valid L2/L3/L4 frames is the caller's (generator's) job.
package datapath

import (
	"fmt"
	"net"
	"time"

	"github.com/asavie/xdp"
)

// AF_XDP UMEM sizing. The chunk size must be a power of two (2048 or page size);
// packets occupy the first Len bytes of a chunk.
const (
	afxdpFrameSize = 2048
	afxdpNumFrames = 4096
	afxdpRingDescs = 2048
	// afxdpRxPollMs bounds an RxPoll so the receive loop observes cancellation.
	afxdpRxPollMs = 200
)

func afxdpOptions() *xdp.SocketOptions {
	return &xdp.SocketOptions{
		NumFrames:              afxdpNumFrames,
		FrameSize:              afxdpFrameSize,
		FillRingNumDescs:       afxdpRingDescs,
		CompletionRingNumDescs: afxdpRingDescs,
		RxRingNumDescs:         afxdpRingDescs,
		TxRingNumDescs:         afxdpRingDescs,
	}
}

// AFXDPTx is the AF_XDP transmit datapath. It needs no XDP program (only RX
// redirection does), just an XSK bound to a NIC queue.
type AFXDPTx struct {
	xsk    *xdp.Socket
	descs  []xdp.Desc // descriptors handed out by the last TxReserve
	submit []xdp.Desc // scratch for TxCommit
	out    []Frame    // scratch for TxReserve
}

// NewAFXDPTx binds an AF_XDP transmit socket to queue on the NIC named ifname.
func NewAFXDPTx(ifname string, queue int) (*AFXDPTx, error) {
	iface, err := net.InterfaceByName(ifname)
	if err != nil {
		return nil, fmt.Errorf("afxdp: interface %q: %w", ifname, err)
	}
	xsk, err := xdp.NewSocket(iface.Index, queue, afxdpOptions())
	if err != nil {
		return nil, fmt.Errorf("afxdp: socket on %s/q%d: %w", ifname, queue, err)
	}
	return &AFXDPTx{
		xsk:    xsk,
		submit: make([]xdp.Desc, 0, afxdpRingDescs),
		out:    make([]Frame, 0, afxdpRingDescs),
	}, nil
}

// Name implements TxDatapath.
func (*AFXDPTx) Name() string { return "afxdp" }

// Caps implements TxDatapath.
func (*AFXDPTx) Caps() Capabilities { return Capabilities{RawL2: true} }

// TxReserve hands out free UMEM frames (aliases, no copy) to fill.
func (a *AFXDPTx) TxReserve(n int) []Frame {
	if free := a.xsk.NumFreeTxSlots(); n > free {
		n = free
	}
	if n <= 0 {
		return nil
	}
	a.descs = a.xsk.GetDescs(n)
	a.out = a.out[:0]
	for i := range a.descs {
		a.out = append(a.out, Frame{Data: a.xsk.GetFrame(a.descs[i])})
	}
	return a.out
}

// TxCommit submits the filled frames to the TX ring and kicks the kernel,
// reaping completions so the UMEM frames recycle.
func (a *AFXDPTx) TxCommit(frames []Frame) (int, error) {
	a.submit = a.submit[:0]
	for i := range frames {
		if frames[i].Len <= 0 {
			continue
		}
		d := a.descs[i]
		d.Len = uint32(frames[i].Len)
		a.submit = append(a.submit, d)
	}
	if len(a.submit) == 0 {
		return 0, nil
	}
	n := a.xsk.Transmit(a.submit)
	if _, _, err := a.xsk.Poll(0); err != nil { // kick TX + reap completions
		return n, fmt.Errorf("afxdp: poll: %w", err)
	}
	return n, nil
}

// Close releases the socket.
func (a *AFXDPTx) Close() error { return a.xsk.Close() }

// AFXDPRx is the AF_XDP receive datapath. It loads the redirect XDP program,
// registers the socket, and keeps the fill ring primed.
type AFXDPRx struct {
	prog    *xdp.Program
	xsk     *xdp.Socket
	ifindex int
	queue   int
	descs   []xdp.Desc // descriptors handed out by the last RxPoll
	out     []Frame    // scratch for RxPoll
}

// NewAFXDPRx binds an AF_XDP receive socket to queue on the NIC named ifname,
// attaching the redirect program so inbound packets reach the socket.
func NewAFXDPRx(ifname string, queue int) (rx *AFXDPRx, err error) {
	iface, err := net.InterfaceByName(ifname)
	if err != nil {
		return nil, fmt.Errorf("afxdp: interface %q: %w", ifname, err)
	}
	prog, err := xdp.NewProgram(queue + 1)
	if err != nil {
		return nil, fmt.Errorf("afxdp: load program: %w", err)
	}
	defer func() {
		if err != nil {
			_ = prog.Detach(iface.Index)
			_ = prog.Close()
		}
	}()
	if err = prog.Attach(iface.Index); err != nil {
		return nil, fmt.Errorf("afxdp: attach program to %s: %w", ifname, err)
	}
	xsk, err := xdp.NewSocket(iface.Index, queue, afxdpOptions())
	if err != nil {
		return nil, fmt.Errorf("afxdp: socket on %s/q%d: %w", ifname, queue, err)
	}
	if err = prog.Register(queue, xsk.FD()); err != nil {
		_ = xsk.Close()
		return nil, fmt.Errorf("afxdp: register socket: %w", err)
	}
	xsk.Fill(xsk.GetDescs(xsk.NumFreeFillSlots())) // prime the fill ring
	return &AFXDPRx{prog: prog, xsk: xsk, ifindex: iface.Index, queue: queue,
		out: make([]Frame, 0, afxdpRingDescs)}, nil
}

// Name implements RxDatapath.
func (*AFXDPRx) Name() string { return "afxdp" }

// Caps implements RxDatapath.
func (*AFXDPRx) Caps() Capabilities { return Capabilities{RawL2: true} }

// RxPoll returns received frames (aliases of the UMEM), refilling the fill ring
// first. It blocks up to afxdpRxPollMs, returning (nil, nil) on timeout so the
// caller can check for cancellation.
func (a *AFXDPRx) RxPoll(max int) ([]Frame, error) {
	if n := a.xsk.NumFreeFillSlots(); n > 0 {
		a.xsk.Fill(a.xsk.GetDescs(n))
	}
	numRx, _, err := a.xsk.Poll(afxdpRxPollMs)
	if err != nil {
		return nil, fmt.Errorf("afxdp: poll: %w", err)
	}
	if numRx == 0 {
		return nil, nil
	}
	if numRx > max {
		numRx = max
	}
	a.descs = a.xsk.Receive(numRx)
	a.out = a.out[:0]
	now := time.Now().UnixNano()
	for i := range a.descs {
		a.out = append(a.out, Frame{
			Data: a.xsk.GetFrame(a.descs[i]),
			Len:  int(a.descs[i].Len),
			Meta: Meta{Nanos: now},
		})
	}
	return a.out, nil
}

// RxRelease returns the polled frames to the fill ring for reuse.
func (a *AFXDPRx) RxRelease([]Frame) { a.xsk.Fill(a.descs) }

// Close unregisters the socket and detaches the program.
func (a *AFXDPRx) Close() error {
	_ = a.prog.Unregister(a.queue)
	err := a.xsk.Close()
	_ = a.prog.Detach(a.ifindex)
	_ = a.prog.Close()
	return err
}

func init() {
	// Register the TX side so senders can select "afxdp" (receivers are built
	// directly, like the UDP listener).
	Registry.Register("afxdp", func(o Options) (TxDatapath, error) {
		return NewAFXDPTx(o.Iface, o.Queue)
	})
}
