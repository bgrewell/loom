// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package voip

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bgrewell/loom/core/accounting"
	"github.com/bgrewell/loom/core/netpath"
	"github.com/bgrewell/loom/core/owd"
	"github.com/bgrewell/loom/core/quality/gilbert"
	"github.com/bgrewell/loom/core/rtp"
	"github.com/bgrewell/loom/core/rtp/codec"
	"github.com/bgrewell/loom/core/rtp/rtcp"
)

const (
	// evenPortAttempts bounds the ephemeral even-port search; after this many
	// odd draws the last (odd) bind is kept — a preference, not a guarantee,
	// since RFC 3550 §11 only recommends the even/odd convention and rtcp-mux
	// makes it cosmetic.
	evenPortAttempts = 8
	// maxCandidates bounds the pre-latch candidate table; packets from
	// further sources are counted as strays outright, so a scanner cannot
	// grow session state.
	maxCandidates = 8
	// maxSeqAdvance is the largest believable extended-sequence advance for
	// per-slot loss accounting, mirroring A.1's MAX_DROPOUT. A larger (or
	// backward) jump means the sender restarted: the playout model re-anchors
	// instead of fabricating thousands of Gilbert losses.
	maxSeqAdvance = 3000
	// rxBufSize holds any RTP/RTCP datagram loom emits (payloads are bounded
	// by codec ptime sizing, far below this).
	rxBufSize = 2048
	// paceResyncSlack is how far the TX pacer may fall behind its absolute
	// schedule before it re-bases instead of bursting to catch up (e.g. after
	// a laptop suspend).
	paceResyncSlack = 20
	// maxAnchorAge bounds how far the playout anchor may recede: past it the
	// anchor slides forward along its own media-clock line, keeping the
	// signed 32-bit timestamp difference far from its ±2³¹-tick wrap
	// (~12.4 h at 48 kHz) on long sessions.
	maxAnchorAge = time.Hour
)

// candKey identifies a pre-latch candidate source.
type candKey struct {
	addr string
	ssrc uint32
}

// candidate is one pre-latch source working through A.1 probation.
type candidate struct {
	stats *rtp.ReceiverStats
	pkts  uint64
}

// MediaSession is one bidirectional RTP/RTCP media session with live G.107
// scoring. Build it with NewMediaSession (which binds the socket, so the
// local address is known before Run), drive it with Run, and read it with
// Metrics at telemetry boundaries — Metrics is safe concurrently with Run.
// See the package comment for the latch, playout, and locking models.
type MediaSession struct {
	cfg     MediaConfig
	owdProv owd.OffsetProvider

	pc        net.PacketConn
	local     netip.AddrPort
	caller    bool          // RemoteRTP set: start media immediately, enforce handshake
	sending   bool          // Direction != RecvOnly
	scoring   bool          // Direction != SendOnly
	ptime     time.Duration // packetization interval
	jb        time.Duration // jitter-buffer nominal depth
	payloadSz int
	src       rtp.PayloadSource
	localSSRC uint32
	cname     string
	seed      int64         // RTCP interval rng seed (crypto/rand; app glue overrides with Options.Seed)
	rtcpMin   time.Duration // Interval Tmin override; 0 selects rtcp.DefaultTmin (tests shorten it)

	acct    accounting.Counters
	started atomic.Bool

	latchedCh chan struct{} // closed when the remote source latches
	inboundCh chan struct{} // closed on first valid inbound RTP or RTCP
	inbound1  sync.Once

	// mu guards everything below (see the package comment's locking section).
	mu sync.Mutex

	// TX state (written by the pacer, read by the RTCP builder and Metrics).
	pkt      *rtp.Packetizer
	txPkts   uint64
	txOctets uint64
	lastTxTS uint32    // RTP timestamp of the most recent packet sent
	lastTxAt time.Time // wall-clock instant it was sent (SR NTP↔RTP mapping)
	// remoteAddr is the RTP/RTCP destination: fixed for callers; for
	// answerers it is learned from the first valid RTCP source (so a
	// recvonly caller, which sends no RTP, still gets return traffic) and
	// overridden by the RTP latch when media arrives.
	remoteAddr net.Addr
	txReady    bool // latchedCh closed (destination known, pacer may start)

	// Latch / RX state.
	latched      bool
	remoteKey    string // latched source's address (Network's own formatting)
	remoteSSRC   uint32
	stats        *rtp.ReceiverStats
	gil          *gilbert.Estimator
	cands        map[candKey]*candidate
	strays       uint64
	lastExtHigh  uint32
	lastExpected uint64 // A.3 expected after the previous counted packet

	// Playout (fixed playout point) state.
	jbAnchored bool
	anchorAt   time.Time // A0
	anchorTS   uint32    // TS0
	discards   uint64
	discPrior  uint64 // interval prior for Metrics

	// Inbound RTCP state.
	lastSRCompact uint32 // CompactNTP of the last SR received (LSR for our reports)
	lastSRArrival time.Time
	rtt           time.Duration
	haveRTT       bool
	owdEst        owd.Estimate
	remoteR       float64
	remoteMOS     float64
	remoteBye     bool

	fatal error

	// closeOnce makes the socket close idempotent between Run's teardown and
	// an explicit Close (built-but-never-run sessions must not leak the port).
	closeOnce sync.Once
	closeErr  error
}

// NewMediaSession binds a media session on n per cfg. The socket is bound
// here — not in Run — so LocalAddr is valid immediately (the app server
// advertises it at configure time). o supplies the clock offset for one-way
// delay; nil selects the labeled RTT/2 fallback.
func NewMediaSession(n netpath.Network, cfg MediaConfig, o owd.OffsetProvider) (*MediaSession, error) {
	if n == nil {
		return nil, errors.New("voip: nil netpath.Network")
	}
	if !cfg.Direction.valid() {
		return nil, fmt.Errorf("voip: invalid direction %d", int(cfg.Direction))
	}
	if cfg.JitterBufferMs < 0 {
		return nil, fmt.Errorf("voip: negative jitter buffer %d ms", cfg.JitterBufferMs)
	}
	c := cfg.Codec
	if c.PayloadBytes == nil || c.SamplesPerPacket == nil {
		if c.Name == "" {
			return nil, errors.New("voip: MediaConfig.Codec is empty (use codec.ByName)")
		}
		row, err := codec.ByName(c.Name)
		if err != nil {
			return nil, fmt.Errorf("voip: %w", err)
		}
		if c.Ptime > 0 {
			row.Ptime = c.Ptime
		}
		c = row
	}
	if c.ClockRate == 0 {
		return nil, fmt.Errorf("voip: codec %q has zero clock rate", c.Name)
	}
	if c.PayloadType > 0x7F {
		return nil, fmt.Errorf("voip: codec %q payload type %d exceeds 7 bits", c.Name, c.PayloadType)
	}
	cfg.Codec = c
	if cfg.JitterBufferMs == 0 {
		cfg.JitterBufferMs = DefaultJitterBufferMs
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = DefaultHandshakeTimeout
	}
	ptime := c.Ptime
	if ptime <= 0 {
		ptime = codec.DefaultPtime
	}
	m := &MediaSession{
		cfg:       cfg,
		owdProv:   o,
		caller:    cfg.RemoteRTP.Port() != 0,
		sending:   cfg.Direction != RecvOnly,
		scoring:   cfg.Direction != SendOnly,
		ptime:     ptime,
		jb:        time.Duration(cfg.JitterBufferMs) * time.Millisecond,
		payloadSz: c.PayloadBytes(ptime),
		gil:       gilbert.New(0),
		cands:     make(map[candKey]*candidate),
		latchedCh: make(chan struct{}),
		inboundCh: make(chan struct{}),
	}
	if m.sending && m.payloadSz <= 0 {
		return nil, fmt.Errorf("voip: codec %q yields no payload at ptime %v", c.Name, ptime)
	}
	m.src = sourceFor(c, m.payloadSz)
	m.pkt = rtp.NewPacketizer(c)
	m.localSSRC = cfg.SSRC
	if m.localSSRC == 0 {
		m.localSSRC = m.pkt.SSRC()
	}
	m.cname = fmt.Sprintf("loom-voip-%08x", m.localSSRC)
	var sb [8]byte
	if _, err := crand.Read(sb[:]); err != nil {
		return nil, fmt.Errorf("voip: crypto/rand unavailable: %w", err)
	}
	m.seed = int64(binary.LittleEndian.Uint64(sb[:]))
	if m.caller {
		m.remoteAddr = udpAddrOf(cfg.RemoteRTP)
	}
	if err := m.bind(n); err != nil {
		return nil, err
	}
	return m, nil
}

// bind opens the single rtcp-mux socket: the configured port exactly when
// given, otherwise an ephemeral port with an even preference.
func (m *MediaSession) bind(n netpath.Network) error {
	host := ""
	if a := m.cfg.LocalRTP.Addr(); a.IsValid() && !a.IsUnspecified() {
		host = a.String()
	}
	if p := m.cfg.LocalRTP.Port(); p != 0 {
		pc, err := n.ListenPacket("udp", net.JoinHostPort(host, strconv.Itoa(int(p))))
		if err != nil {
			return fmt.Errorf("voip: bind %s:%d: %w", host, p, err)
		}
		m.pc, m.local = pc, addrPortOf(pc.LocalAddr())
		return nil
	}
	var pc net.PacketConn
	for i := 0; i < evenPortAttempts; i++ {
		c, err := n.ListenPacket("udp", net.JoinHostPort(host, "0"))
		if err != nil {
			return fmt.Errorf("voip: bind ephemeral: %w", err)
		}
		if addrPortOf(c.LocalAddr()).Port()%2 == 0 {
			pc = c
			break
		}
		if i == evenPortAttempts-1 {
			pc = c // keep the odd one; the convention is best-effort
			break
		}
		_ = c.Close()
	}
	m.pc, m.local = pc, addrPortOf(pc.LocalAddr())
	return nil
}

// LocalAddr returns the bound RTP (and, via rtcp-mux, RTCP) address. Networks
// whose addresses are not IP-formed (the in-memory fabric) report the
// unspecified IPv4 address with the real port.
func (m *MediaSession) LocalAddr() netip.AddrPort { return m.local }

// Counters exposes the session's live byte/packet totals, counting both
// directions of the media plane (RTP and RTCP, sent and received);
// per-direction packet splits live in Metrics.
func (m *MediaSession) Counters() *accounting.Counters { return &m.acct }

// StrayPackets counts packets dropped by the latch: sources that lost the
// rendezvous race, failed RTP validity, or appeared after the latch —
// including RTCP datagrams from any address other than the latched source.
func (m *MediaSession) StrayPackets() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.strays
}

// Close releases the session's socket. It exists for the built-but-never-run
// path (a server whose flow is torn down between Configure and Start must
// not leak its advertised port); calling it while Run is active terminates
// the session through the socket-failure path, and calling it after Run has
// finished is a no-op. Safe to call multiple times.
func (m *MediaSession) Close() error { return m.closePC() }

// closePC closes the socket exactly once, shared by Run's teardown and Close.
func (m *MediaSession) closePC() error {
	m.closeOnce.Do(func() { m.closeErr = m.pc.Close() })
	return m.closeErr
}

// Run drives the session until ctx is cancelled: TX pacing at ptime, the RX
// loop with latch/playout/Gilbert accounting, and the RTCP scheduler. On
// cancellation it sends a best-effort RTCP BYE, closes the socket, and
// returns nil; it returns a *HandshakeError when a caller sees no return
// traffic in time, and the socket error when the socket fails (or is closed
// externally) mid-session. Run may be called once.
func (m *MediaSession) Run(ctx context.Context) error {
	if !m.started.CompareAndSwap(false, true) {
		return errors.New("voip: session already run")
	}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go m.rxLoop(rctx, cancel, &wg)
	go m.rtcpLoop(rctx, &wg)
	if m.sending {
		wg.Add(1)
		go m.txLoop(rctx, &wg)
	}
	// Shutdown watchdog: a blocked ReadFrom does not watch ctx, so kick it
	// with an immediate read deadline once we are cancelled.
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-rctx.Done()
		_ = m.pc.SetReadDeadline(time.Now())
	}()

	var hsErr error
	if m.caller {
		t := time.NewTimer(m.cfg.HandshakeTimeout)
		select {
		case <-m.inboundCh:
			t.Stop()
			<-rctx.Done()
		case <-t.C:
			hsErr = &HandshakeError{Timeout: m.cfg.HandshakeTimeout}
		case <-rctx.Done():
			t.Stop()
		}
	} else {
		<-rctx.Done()
	}
	cancel()
	wg.Wait()

	m.sendReport(true) // best-effort BYE compound before the socket goes away
	_ = m.closePC()

	m.mu.Lock()
	fatal := m.fatal
	m.mu.Unlock()
	switch {
	case hsErr != nil:
		return hsErr
	case fatal != nil && ctx.Err() == nil:
		return fatal
	default:
		return nil
	}
}

// txLoop paces one RTP packet per ptime on an absolute-deadline schedule:
// each iteration sleeps until the next deadline computed from the previous
// one (never a relative time.Ticker, never a spin), so pacing error does not
// accumulate; if the loop falls further behind than paceResyncSlack packets
// it re-bases rather than bursting.
func (m *MediaSession) txLoop(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	if !m.caller {
		// Answerer: no destination until the latch (or first valid RTCP
		// source, for RTP-silent peers) decides one.
		select {
		case <-ctx.Done():
			return
		case <-m.latchedCh:
		}
	}

	payload := make([]byte, m.payloadSz)
	buf := make([]byte, rtp.HeaderLen+m.payloadSz)
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	next := time.Now()
	var idx uint64
	for {
		pl := m.src.Fill(payload, idx)
		m.mu.Lock()
		n := m.pkt.Next(buf, payload[:pl])
		if n > 0 {
			if m.cfg.SSRC != 0 {
				// The Packetizer draws its own random SSRC (RFC 3550 §8); an
				// SDP-assigned one is patched into the marshaled header.
				binary.BigEndian.PutUint32(buf[8:12], m.cfg.SSRC)
			}
			m.lastTxTS = binary.BigEndian.Uint32(buf[4:8])
			m.lastTxAt = time.Now()
			m.txPkts++
			m.txOctets += uint64(pl)
		}
		// Re-read the destination each packet: an answerer's provisional
		// RTCP-learned address is superseded by the RTP latch.
		dst := m.remoteAddr
		m.mu.Unlock()
		if n > 0 && dst != nil {
			if _, err := m.pc.WriteTo(buf[:n], dst); err == nil {
				m.acct.Add(uint64(n))
			}
		}
		idx++
		next = next.Add(m.ptime)
		d := time.Until(next)
		if d <= 0 {
			// The catch-up path must observe cancellation too: a pacer that
			// is persistently behind schedule (tiny ptime, blocking writes)
			// never reaches the select below.
			if ctx.Err() != nil {
				return
			}
			if d < -paceResyncSlack*m.ptime {
				next = time.Now()
			}
			continue
		}
		timer.Reset(d)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// metaReader is the optional net.PacketConn extension (structurally matching
// netpath/dgram.MetaConn and the memory fabric) whose ReadFromMeta returns the
// datagram's receive timestamp stamped by the underlying datapath. When the
// mux socket provides it, jitter and one-way-delay measurement anchor at the
// wire arrival instead of this goroutine's dequeue time, so in-process
// queueing (rings, channel handoffs, scheduling) does not masquerade as
// network jitter. A zero timestamp falls back to time.Now().
type metaReader interface {
	ReadFromMeta(p []byte) (int, net.Addr, time.Time, error)
}

// rxLoop reads the mux socket until cancellation or socket failure,
// classifying each datagram per RFC 5761 and dispatching to the RTP latch/
// stats path or the RTCP processor. Arrival times prefer the datapath's
// receive timestamp when the socket implements metaReader. A read error while
// ctx is live is fatal: it is recorded and cancels the session (deterministic
// termination on socket close).
func (m *MediaSession) rxLoop(ctx context.Context, cancel context.CancelFunc, wg *sync.WaitGroup) {
	defer wg.Done()
	buf := make([]byte, rxBufSize)
	mr, _ := m.pc.(metaReader)
	for {
		var (
			n     int
			from  net.Addr
			stamp time.Time
			err   error
		)
		if mr != nil {
			n, from, stamp, err = mr.ReadFromMeta(buf)
		} else {
			n, from, err = m.pc.ReadFrom(buf)
		}
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				if ctx.Err() != nil {
					return
				}
				_ = m.pc.SetReadDeadline(time.Time{})
				continue
			}
			if ctx.Err() == nil {
				m.mu.Lock()
				if m.fatal == nil {
					m.fatal = fmt.Errorf("voip: media socket: %w", err)
				}
				m.mu.Unlock()
				cancel()
			}
			return
		}
		arrival := stamp
		if arrival.IsZero() {
			arrival = time.Now()
		}
		m.acct.Add(uint64(n))
		pkt := buf[:n]
		if rtcp.IsRTCP(pkt) {
			m.handleRTCP(pkt, arrival, from)
			continue
		}
		m.handleRTP(pkt, arrival, from)
	}
}

// signalInbound marks the handshake satisfied (first valid RTP or RTCP).
func (m *MediaSession) signalInbound() {
	m.inbound1.Do(func() { close(m.inboundCh) })
}

// handleRTP runs one inbound datagram through validity, the latch, receiver
// statistics, and — when scoring — the playout/Gilbert accounting.
func (m *MediaSession) handleRTP(b []byte, arrival time.Time, from net.Addr) {
	h, off, err := rtp.ParseHeader(b)
	if err != nil || h.PayloadType != m.cfg.Codec.PayloadType {
		m.mu.Lock()
		m.strays++
		m.mu.Unlock()
		return
	}
	payloadLen := len(b) - off

	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.latched {
		m.latchLocked(h, payloadLen, arrival, from)
		return
	}
	if h.SSRC != m.remoteSSRC || from.String() != m.remoteKey {
		m.strays++
		return
	}
	// The handshake is satisfied only by the latched source (or by the latch
	// completing, in latchLocked): a stray with a common payload type must
	// not suppress the caller's typed handshake error.
	m.signalInbound()
	recvBefore, dupBefore, _, _ := m.stats.Counts()
	m.stats.Observe(h, payloadLen, arrival)
	if !m.scoring {
		return
	}
	recvAfter, dupAfter, exp, after := m.stats.Counts()
	// An A.1 resync resets the loss baseline, so expected shrinking is the
	// restart signal — it fires wherever the re-randomized sequence base
	// lands, forward or backward of the old extended max.
	restart := exp < m.lastExpected
	m.lastExpected = exp
	delta := int32(after - m.lastExtHigh)
	m.lastExtHigh = after
	switch {
	case recvAfter == recvBefore:
		// Probation or unconfirmed jump: not counted, no playout slot.
	case restart || delta > maxSeqAdvance:
		// Sender restart (A.1 resync): the timestamp base changed, so the
		// playout anchor is meaningless. Re-anchor instead of fabricating a
		// discard burst; the skipped slots are not fed as losses either.
		m.jbAnchored = false
		m.observePlayoutLocked(h, arrival)
	case delta <= 0:
		if dupAfter != dupBefore {
			return // duplicate: its slot already has an outcome
		}
		// Reordered straggler behind the max: its slot was already fed to
		// the Gilbert estimator as a loss when the sequence advanced past it
		// (a documented approximation for on-time reordering), and
		// ReceiverStats has now credited it back as received — so one that
		// also missed its playout deadline must be recorded as a discard, or
		// Ppl undercounts exactly the reordering-under-path-switch case.
		if m.jbAnchored && m.pastDeadlineLocked(h, arrival) {
			m.discards++
		}
	default:
		for i := int32(1); i < delta; i++ {
			m.gil.Observe(true, arrival)
		}
		m.observePlayoutLocked(h, arrival)
	}
}

// latchLocked advances the pre-latch candidate machinery: the first
// (srcAddr, SSRC) source to pass A.1 probation wins; everyone else becomes
// stray. Callers hold mu.
func (m *MediaSession) latchLocked(h rtp.Header, payloadLen int, arrival time.Time, from net.Addr) {
	key := candKey{addr: from.String(), ssrc: h.SSRC}
	c := m.cands[key]
	if c == nil {
		if len(m.cands) >= maxCandidates {
			m.strays++
			return
		}
		c = &candidate{stats: rtp.NewReceiverStats(m.cfg.Codec.ClockRate)}
		m.cands[key] = c
	}
	c.pkts++
	c.stats.Observe(h, payloadLen, arrival)
	cum := c.stats.Cumulative()
	if cum.Expected == 0 {
		return // still in probation
	}
	m.latched = true
	m.stats = c.stats
	m.remoteSSRC = h.SSRC
	m.remoteKey = key.addr
	m.lastExtHigh = cum.ExtHighestSeq
	m.lastExpected = cum.Expected
	if !m.caller {
		// The media source wins over any provisional RTCP-learned address.
		m.remoteAddr = from
	}
	for k, o := range m.cands {
		if k != key {
			m.strays += o.pkts
		}
	}
	m.cands = nil
	m.startTxLocked()
	m.signalInbound()
	if m.scoring {
		m.observePlayoutLocked(h, arrival)
	}
}

// startTxLocked releases the answerer's pacer exactly once, whether the
// destination came from the RTP latch or from the first valid RTCP source.
// Callers hold mu.
func (m *MediaSession) startTxLocked() {
	if !m.txReady {
		m.txReady = true
		close(m.latchedCh)
	}
}

// observePlayoutLocked applies the fixed playout-point model to one counted
// packet: the first packet anchors (A0, TS0); later packets whose arrival is
// past A0 + (TS−TS0)/clockRate + jb are discards, fed to the Gilbert
// estimator as losses. Callers hold mu.
func (m *MediaSession) observePlayoutLocked(h rtp.Header, arrival time.Time) {
	if !m.jbAnchored {
		m.jbAnchored = true
		m.anchorAt = arrival
		m.anchorTS = h.Timestamp
		m.gil.Observe(false, arrival)
		return
	}
	elapsed := ticksToDuration(int32(h.Timestamp-m.anchorTS), m.cfg.Codec.ClockRate)
	deadline := m.anchorAt.Add(elapsed + m.jb)
	if arrival.After(deadline) {
		m.discards++
		m.gil.Observe(true, arrival)
	} else {
		m.gil.Observe(false, arrival)
	}
	if elapsed > maxAnchorAge {
		// Slide the anchor along its own media-clock line (the deadline
		// arithmetic is unchanged) so the signed 32-bit timestamp difference
		// never approaches its wrap on long sessions.
		m.anchorAt = m.anchorAt.Add(elapsed)
		m.anchorTS = h.Timestamp
	}
}

// pastDeadlineLocked reports whether an anchored packet missed its playout
// deadline. Callers hold mu with jbAnchored true.
func (m *MediaSession) pastDeadlineLocked(h rtp.Header, arrival time.Time) bool {
	elapsed := ticksToDuration(int32(h.Timestamp-m.anchorTS), m.cfg.Codec.ClockRate)
	return arrival.After(m.anchorAt.Add(elapsed + m.jb))
}

// ticksToDuration converts a signed RTP-timestamp difference to wall time.
func ticksToDuration(ticks int32, clockRate uint32) time.Duration {
	return time.Duration(int64(ticks) * int64(time.Second) / int64(clockRate))
}

// durationToTicks converts elapsed wall time to RTP timestamp units,
// split to avoid overflow on long sessions.
func durationToTicks(d time.Duration, clockRate uint32) uint32 {
	sec := int64(d / time.Second)
	rem := int64(d % time.Second)
	return uint32(sec*int64(clockRate) + rem*int64(clockRate)/int64(time.Second))
}

// rtcpLoop schedules compound reports on the RFC 3550 §6.3 randomized
// interval, including the §6.3.3 timer-reconsideration recheck at expiry the
// Interval type requires of its caller.
func (m *MediaSession) rtcpLoop(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	rng := rand.New(rand.NewSource(m.seed))
	iv := rtcp.Interval{Members: 2, WeSent: m.sending, Initial: true, Tmin: m.rtcpMin}
	iv.Senders = m.senderCount()
	tp := time.Now()
	tn := tp.Add(iv.Next(rng))
	timer := time.NewTimer(time.Until(tn))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-timer.C:
			iv.Senders = m.senderCount()
			if tn2 := tp.Add(iv.Next(rng)); tn2.After(now) {
				tn = tn2 // reconsidered: not yet, re-arm without sending
				timer.Reset(time.Until(tn))
				continue
			}
			m.sendReport(false)
			iv.Initial = false
			tp = time.Now()
			tn = tp.Add(iv.Next(rng))
			timer.Reset(time.Until(tn))
		}
	}
}

// senderCount estimates the session's sender population for the RTCP
// bandwidth split: us if we send media, plus the peer once latched.
func (m *MediaSession) senderCount() int {
	n := 0
	if m.sending {
		n++
	}
	m.mu.Lock()
	if m.latched {
		n++
	}
	m.mu.Unlock()
	return n
}

// setRTCPTmin overrides the RTCP minimum interval. It exists for tests (and
// in-package tuning) and must be called before Run; the wire default is
// rtcp.DefaultTmin per RFC 3550 §6.2.
func (m *MediaSession) setRTCPTmin(d time.Duration) { m.rtcpMin = d }

// udpAddrOf converts a netip.AddrPort to the *net.UDPAddr PacketConn.WriteTo
// wants; port-only routed networks (memory) ignore the host part.
func udpAddrOf(ap netip.AddrPort) *net.UDPAddr {
	return &net.UDPAddr{IP: ap.Addr().AsSlice(), Port: int(ap.Port()), Zone: ap.Addr().Zone()}
}

// addrPortOf extracts a netip.AddrPort from a net.Addr, degrading to the
// unspecified IPv4 address for networks whose address strings are not
// IP-formed (the in-memory fabric's "mem:port").
func addrPortOf(a net.Addr) netip.AddrPort {
	if ua, ok := a.(*net.UDPAddr); ok {
		return ua.AddrPort()
	}
	host, ps, err := net.SplitHostPort(a.String())
	if err != nil {
		return netip.AddrPort{}
	}
	port, err := strconv.Atoi(ps)
	if err != nil || port < 0 || port > 65535 {
		return netip.AddrPort{}
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		ip = netip.IPv4Unspecified()
	}
	return netip.AddrPortFrom(ip, uint16(port))
}
