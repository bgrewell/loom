// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package datapath

import (
	"net"
	"sync"
	"time"
)

// tcpReadSize is the per-read buffer for the TCP receiver. TCP is a byte stream,
// so packet boundaries are meaningless on receive — large reads minimise syscalls
// (the lever that matters for receive throughput).
const tcpReadSize = 256 * 1024

// tcpRxQueue bounds the in-flight read buffers / queued chunks.
const tcpRxQueue = 256

// TCPListener is a receive-side datapath over TCP: it accepts connections and
// reads their bytes into pooled buffers, which RxPoll hands to the receiver to
// account. It complements the UDP listener for the socket backend family; a flow
// with `datapath: tcp` lands here on the receiving agent.
//
// Connections are accepted and read by background goroutines into a channel, so
// one listener handles one or many senders; RxPoll drains whatever has arrived.
type TCPListener struct {
	ln   net.Listener
	recv chan []byte   // chunks read off connections
	free chan []byte   // recycled read buffers
	done chan struct{} // closed by Close
	out  []Frame       // RxPoll scratch (receiver goroutine only)

	mu        sync.Mutex
	conns     map[net.Conn]struct{}
	closeOnce sync.Once
}

// ListenTCP binds a TCP listener at addr (use ":0" for an ephemeral port) and
// starts accepting. frameSize is unused (TCP reads are stream-sized).
func ListenTCP(addr string, frameSize int) (*TCPListener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	l := &TCPListener{
		ln:    ln,
		recv:  make(chan []byte, tcpRxQueue),
		free:  make(chan []byte, tcpRxQueue),
		done:  make(chan struct{}),
		conns: make(map[net.Conn]struct{}),
	}
	go l.acceptLoop()
	return l, nil
}

// Port returns the bound local TCP port.
func (l *TCPListener) Port() int {
	if a, ok := l.ln.Addr().(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}

// Name implements RxDatapath.
func (*TCPListener) Name() string { return "tcp-listen" }

// Caps implements RxDatapath.
func (*TCPListener) Caps() Capabilities { return Capabilities{} }

func (l *TCPListener) acceptLoop() {
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			return // listener closed
		}
		l.mu.Lock()
		l.conns[conn] = struct{}{}
		l.mu.Unlock()
		go l.readLoop(conn)
	}
}

func (l *TCPListener) readLoop(conn net.Conn) {
	defer func() {
		l.mu.Lock()
		delete(l.conns, conn)
		l.mu.Unlock()
		_ = conn.Close()
	}()
	for {
		buf := l.getBuf()
		n, err := conn.Read(buf)
		if n > 0 {
			select {
			case l.recv <- buf[:n]:
			case <-l.done:
				return
			}
		} else {
			l.putBuf(buf)
		}
		if err != nil {
			return
		}
	}
}

func (l *TCPListener) getBuf() []byte {
	select {
	case b := <-l.free:
		return b[:cap(b)]
	default:
		return make([]byte, tcpReadSize)
	}
}

func (l *TCPListener) putBuf(b []byte) {
	select {
	case l.free <- b[:cap(b)]:
	default: // pool full; let it go
	}
}

// RxPoll returns up to max chunks read off the connections. The first read blocks
// up to recvDeadline so the receiver can observe cancellation; (nil, nil) on a
// timeout means "nothing yet, keep polling".
func (l *TCPListener) RxPoll(max int) ([]Frame, error) {
	if max < 1 {
		max = 1
	}
	out := l.out[:0]
	timer := time.NewTimer(recvDeadline)
	defer timer.Stop()
	select {
	case buf := <-l.recv:
		out = append(out, frameOf(buf))
	case <-timer.C:
		l.out = out
		return nil, nil
	case <-l.done:
		l.out = out
		return nil, net.ErrClosed
	}
	for len(out) < max {
		select {
		case buf := <-l.recv:
			out = append(out, frameOf(buf))
		default:
			l.out = out
			return out, nil
		}
	}
	l.out = out
	return out, nil
}

func frameOf(buf []byte) Frame {
	return Frame{Data: buf, Len: len(buf), Meta: Meta{Nanos: time.Now().UnixNano()}}
}

// RxRelease recycles the polled frames' buffers.
func (l *TCPListener) RxRelease(frames []Frame) {
	for i := range frames {
		l.putBuf(frames[i].Data)
	}
}

// Close stops accepting, unblocks readers, and closes all connections.
func (l *TCPListener) Close() error {
	l.closeOnce.Do(func() { close(l.done) })
	err := l.ln.Close()
	l.mu.Lock()
	for c := range l.conns {
		_ = c.Close()
	}
	l.mu.Unlock()
	return err
}
