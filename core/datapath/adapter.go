// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import "time"

// The SinglePacket adapters bridge a legacy single-packet Datapath (Send/Recv)
// to the batch TxDatapath / RxDatapath (ADR-0019), so the pump and receiver can
// run the new interface before each backend is natively migrated. They are not
// zero-copy — they reuse one frame buffer and call the legacy Send/Recv — but
// they allocate nothing per packet after construction.

// singlePacketTx wraps a legacy Datapath as a TxDatapath.
type singlePacketTx struct {
	dp    Datapath
	frame [1]Frame
	buf   []byte
}

// SinglePacketTx adapts a legacy Datapath to a TxDatapath, reserving frames of
// frameSize bytes. frameSize must be at least as large as the largest packet the
// generator will produce.
func SinglePacketTx(dp Datapath, frameSize int) TxDatapath {
	if frameSize < 1 {
		frameSize = 1500
	}
	a := &singlePacketTx{dp: dp, buf: make([]byte, frameSize)}
	return a
}

func (a *singlePacketTx) Name() string       { return a.dp.Name() }
func (a *singlePacketTx) Caps() Capabilities { return a.dp.Caps() }
func (a *singlePacketTx) Close() error       { return a.dp.Close() }

func (a *singlePacketTx) TxReserve(n int) []Frame {
	if n <= 0 {
		return nil
	}
	a.frame[0] = Frame{Data: a.buf[:cap(a.buf)]}
	return a.frame[:1]
}

func (a *singlePacketTx) TxCommit(frames []Frame) (int, error) {
	sent := 0
	for i := range frames {
		if frames[i].Len <= 0 {
			continue
		}
		if _, err := a.dp.Send(frames[i].Data[:frames[i].Len]); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, nil
}

// singlePacketRx wraps a legacy Datapath as an RxDatapath.
type singlePacketRx struct {
	dp    Datapath
	frame [1]Frame
	buf   []byte
}

// SinglePacketRx adapts a legacy Datapath to an RxDatapath, reading into a buffer
// of frameSize bytes.
func SinglePacketRx(dp Datapath, frameSize int) RxDatapath {
	if frameSize < 1 {
		frameSize = 1500
	}
	return &singlePacketRx{dp: dp, buf: make([]byte, frameSize)}
}

func (a *singlePacketRx) Name() string       { return a.dp.Name() }
func (a *singlePacketRx) Caps() Capabilities { return a.dp.Caps() }
func (a *singlePacketRx) Close() error       { return a.dp.Close() }

func (a *singlePacketRx) RxPoll(max int) ([]Frame, error) {
	if max <= 0 {
		return nil, nil
	}
	n, err := a.dp.Recv(a.buf[:cap(a.buf)])
	if err != nil {
		return nil, err // includes net timeouts; the caller checks Timeout()
	}
	a.frame[0] = Frame{Data: a.buf[:n], Len: n, Meta: Meta{Nanos: time.Now().UnixNano()}}
	return a.frame[:1], nil
}

func (a *singlePacketRx) RxRelease([]Frame) {} // the single buffer is reused on the next poll
