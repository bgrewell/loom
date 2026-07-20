// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

//go:build !loom_nonetstack

package netstack

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bgrewell/loom/core/datapath"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	gstack "gvisor.dev/gvisor/pkg/tcpip/stack"
)

const (
	// rxBatch is the frame batch size per RxPoll.
	rxBatch = 64
	// pollIdleDelay paces the receive loop when a non-blocking backend (the
	// memory datapath) returns an empty poll, so an idle stack doesn't spin.
	pollIdleDelay = 50 * time.Microsecond
)

// dpEndpoint implements stack.LinkEndpoint directly over loom's frame
// contract (ADR-0019): WritePackets copies outbound packets into TxReserve'd
// frames and TxCommits the batch, and one receive goroutine loops
// RxPoll → DeliverNetworkPacket → RxRelease. There is no channel.Endpoint in
// between, so each packet is copied exactly once per direction (into a
// datapath-owned TX frame; out of a borrowed RX frame — both copies are the
// borrow contract's, not an adapter's).
//
// It is a pure L3 endpoint: no link addresses, no ARP, no link header —
// frames are complete IP packets, matching tunneled inner-IP traffic.
// Inbound frames dispatch on the IP version nibble (IPv4/IPv6); anything else
// is dropped and counted.
type dpEndpoint struct {
	tx  datapath.TxDatapath
	rx  datapath.RxDatapath
	mtu atomic.Uint32

	// txMu serializes TxReserve/TxCommit pairs (the datapath contract).
	txMu sync.Mutex

	mu         sync.Mutex
	dispatcher gstack.NetworkDispatcher
	stop       chan struct{}
	closed     bool
	onClose    func()
	wg         sync.WaitGroup

	dropNonIP atomic.Uint64 // inbound frames that were neither IPv4 nor IPv6
}

var _ gstack.LinkEndpoint = (*dpEndpoint)(nil)

// newDPEndpoint builds the endpoint; the receive loop starts on Attach.
func newDPEndpoint(tx datapath.TxDatapath, rx datapath.RxDatapath, mtu uint32) *dpEndpoint {
	e := &dpEndpoint{tx: tx, rx: rx}
	e.mtu.Store(mtu)
	return e
}

// MTU implements stack.LinkEndpoint.
func (e *dpEndpoint) MTU() uint32 { return e.mtu.Load() }

// SetMTU implements stack.LinkEndpoint.
func (e *dpEndpoint) SetMTU(mtu uint32) { e.mtu.Store(mtu) }

// MaxHeaderLength implements stack.LinkEndpoint: no link-layer header.
func (*dpEndpoint) MaxHeaderLength() uint16 { return 0 }

// LinkAddress implements stack.LinkEndpoint: a pure L3 endpoint has none.
func (*dpEndpoint) LinkAddress() tcpip.LinkAddress { return "" }

// SetLinkAddress implements stack.LinkEndpoint: no-op, there is no link layer.
func (*dpEndpoint) SetLinkAddress(tcpip.LinkAddress) {}

// Capabilities implements stack.LinkEndpoint.
func (*dpEndpoint) Capabilities() gstack.LinkEndpointCapabilities { return gstack.CapabilityNone }

// ARPHardwareType implements stack.LinkEndpoint: ARP-free.
func (*dpEndpoint) ARPHardwareType() header.ARPHardwareType { return header.ARPHardwareNone }

// AddHeader implements stack.LinkEndpoint: no link-layer header to add.
func (*dpEndpoint) AddHeader(*gstack.PacketBuffer) {}

// ParseHeader implements stack.LinkEndpoint: no link-layer header to parse.
func (*dpEndpoint) ParseHeader(*gstack.PacketBuffer) bool { return true }

// Attach implements stack.LinkEndpoint: it starts the receive goroutine
// delivering to dispatcher, or — with a nil dispatcher (NIC removal) — stops
// it and waits for it to exit.
func (e *dpEndpoint) Attach(dispatcher gstack.NetworkDispatcher) {
	e.mu.Lock()
	stop := e.stop
	e.stop = nil
	e.dispatcher = nil
	e.mu.Unlock()
	if stop != nil {
		close(stop)
		e.wg.Wait()
	}
	if dispatcher == nil {
		return
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.dispatcher = dispatcher
	stop = make(chan struct{})
	e.stop = stop
	e.wg.Add(1)
	e.mu.Unlock()
	go e.rxLoop(dispatcher, stop)
}

// stopRx halts the receive goroutine and waits for it to exit, without
// closing the endpoint. The Stack calls it before gVisor's Stack.Destroy:
// Destroy's Wait phase holds stack.mu (write) while it blocks in Attach(nil)
// on this same goroutine, and a delivery in flight can block acquiring
// stack.mu (read) inside FindRoute — an RST reply to a just-aborted
// endpoint's orphan segment, or an ICMP echo reply — so letting Destroy be
// the one to stop the loop can deadlock permanently. After stopRx returns no
// delivery is in flight and Destroy's Attach(nil)/Wait find nothing to wait
// for.
func (e *dpEndpoint) stopRx() {
	e.mu.Lock()
	stop := e.stop
	e.stop = nil
	e.dispatcher = nil
	e.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	e.wg.Wait()
}

// IsAttached implements stack.LinkEndpoint.
func (e *dpEndpoint) IsAttached() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dispatcher != nil
}

// Wait implements stack.LinkEndpoint: it blocks until the receive goroutine
// has exited.
func (e *dpEndpoint) Wait() { e.wg.Wait() }

// Close implements stack.LinkEndpoint: it stops the receive loop and runs the
// on-close action. It does not close the datapaths — the Stack owns those.
func (e *dpEndpoint) Close() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	stop := e.stop
	e.stop = nil
	e.dispatcher = nil
	onClose := e.onClose
	e.mu.Unlock()
	if stop != nil {
		close(stop)
		e.wg.Wait()
	}
	if onClose != nil {
		onClose()
	}
}

// SetOnCloseAction implements stack.LinkEndpoint.
func (e *dpEndpoint) SetOnCloseAction(action func()) {
	e.mu.Lock()
	e.onClose = action
	e.mu.Unlock()
}

// frameWriter is an io.Writer over a frame's remaining bytes, so packet data
// views copy straight into datapath-owned memory (no intermediate buffer).
type frameWriter struct {
	b []byte
	n int
}

// Write implements io.Writer.
func (w *frameWriter) Write(p []byte) (int, error) {
	c := copy(w.b[w.n:], p)
	w.n += c
	if c < len(p) {
		return c, io.ErrShortWrite
	}
	return c, nil
}

// WritePackets implements stack.LinkWriter: each packet is copied once into a
// reserved frame (headers, then payload views) and the batch is committed.
// When the TX ring cannot take the whole batch the accepted count is returned
// with ErrNoBufferSpace; the transports retransmit.
func (e *dpEndpoint) WritePackets(pkts gstack.PacketBufferList) (int, tcpip.Error) {
	list := pkts.AsSlice()
	e.txMu.Lock()
	defer e.txMu.Unlock()
	frames := e.tx.TxReserve(len(list))
	if len(frames) == 0 {
		return 0, &tcpip.ErrNoBufferSpace{}
	}
	n := 0
	for i := range frames {
		pkt := list[n]
		f := &frames[i]
		// Bound by len(f.Data) — the frame's usable size — never cap:
		// slab-backed backends hand out frames whose cap runs into
		// neighboring frames' memory.
		if pkt.Size() > len(f.Data) {
			break // oversized for this backend's frames; drop the tail of the batch
		}
		w := frameWriter{b: f.Data}
		_, _ = w.Write(pkt.NetworkHeader().Slice())
		_, _ = w.Write(pkt.TransportHeader().Slice())
		_, _ = pkt.Data().ReadTo(&w, true /* peek */)
		f.Len = w.n
		n++
	}
	sent, err := e.tx.TxCommit(frames[:n])
	if err != nil {
		return sent, &tcpip.ErrAborted{}
	}
	if sent < len(list) {
		return sent, &tcpip.ErrNoBufferSpace{}
	}
	return sent, nil
}

// rxLoop is the endpoint's single receive goroutine: poll, deliver, release,
// until stop. Blocking backends bound their own poll (returning a timeout
// net.Error); non-blocking backends (memory) return empty polls, paced by
// pollIdleDelay.
func (e *dpEndpoint) rxLoop(d gstack.NetworkDispatcher, stop chan struct{}) {
	defer e.wg.Done()
	for {
		select {
		case <-stop:
			return
		default:
		}
		frames, err := e.rx.RxPoll(rxBatch)
		if len(frames) > 0 {
			for i := range frames {
				e.inject(d, &frames[i])
			}
			e.rx.RxRelease(frames)
		}
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue // poll window elapsed; recheck stop
			}
			select {
			case <-stop:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(pollIdleDelay) // unexpected error: don't spin
			continue
		}
		if len(frames) == 0 {
			select {
			case <-stop:
				return
			default:
			}
			time.Sleep(pollIdleDelay)
		}
	}
}

// inject delivers one received frame to the stack, dispatching on the IP
// version nibble. The frame's bytes are copied into the packet buffer before
// the frame goes back to the datapath (borrow contract, ADR-0019).
func (e *dpEndpoint) inject(d gstack.NetworkDispatcher, f *datapath.Frame) {
	data := f.Data
	if f.Len < len(data) {
		data = data[:f.Len]
	}
	if len(data) == 0 {
		e.dropNonIP.Add(1)
		return
	}
	var proto tcpip.NetworkProtocolNumber
	switch data[0] >> 4 {
	case 4:
		proto = header.IPv4ProtocolNumber
	case 6:
		proto = header.IPv6ProtocolNumber
	default:
		e.dropNonIP.Add(1)
		return
	}
	pkt := gstack.NewPacketBuffer(gstack.PacketBufferOptions{
		Payload: buffer.MakeWithData(append([]byte(nil), data...)),
	})
	d.DeliverNetworkPacket(proto, pkt)
	pkt.DecRef()
}
