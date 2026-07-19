// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package voip

import (
	"math"
	"net"
	"time"

	"github.com/bgrewell/loom/core/owd"
	"github.com/bgrewell/loom/core/quality/emodel"
	"github.com/bgrewell/loom/core/quality/gilbert"
	"github.com/bgrewell/loom/core/rtp/rtcp"
)

// ntpEpochOffset is the 1900→1970 epoch shift (RFC 3550 §4), matching
// rtcp.NTPNow's convention.
const ntpEpochOffset = 2208988800

// rxConfigJBFixed is the RFC 3611 §4.7.6 RX config byte this session
// reports: PLC unspecified (bits 7–6 = 00), jitter buffer non-adaptive
// (bits 5–4 = 10), JB rate 0.
const rxConfigJBFixed = 0x20

// sendReport marshals and sends one compound RTCP packet (SR or RR first,
// SDES CNAME, XR VoIP metrics once reception is being scored, and a BYE when
// final). It is a no-op while no destination is known (unlatched answerer).
func (m *MediaSession) sendReport(final bool) {
	m.mu.Lock()
	dst := m.remoteAddr
	if dst == nil {
		m.mu.Unlock()
		return
	}
	pkts := m.buildCompoundLocked(time.Now(), final)
	m.mu.Unlock()
	b, err := rtcp.MarshalCompound(pkts...)
	if err != nil {
		return
	}
	if _, err := m.pc.WriteTo(b, dst); err == nil {
		m.acct.Add(uint64(len(b)))
	}
}

// buildCompoundLocked assembles the compound per RFC 3550 §6.1: SR when we
// have sent media (with the NTP↔RTP mapping anchored at the first packet),
// RR otherwise; a report block about the latched source with LSR/DLSR from
// its last SR; mandatory SDES CNAME; XR VoIP metrics carrying the live
// R/MOS×10; BYE on teardown. Callers hold mu.
func (m *MediaSession) buildCompoundLocked(now time.Time, final bool) []rtcp.Packet {
	var blocks []rtcp.ReportBlock
	if m.latched {
		var lsr, dlsr uint32
		if m.lastSRCompact != 0 {
			lsr = m.lastSRCompact
			dlsr = rtcp.DLSRFromDuration(now.Sub(m.lastSRArrival))
		}
		blocks = append(blocks, rtcp.NewReportBlock(m.remoteSSRC, m.stats.Report(), lsr, dlsr))
	}
	var first rtcp.Packet
	if m.txPkts > 0 {
		sec, frac := rtcp.NTPNow(now)
		first = &rtcp.SenderReport{
			SSRC:    m.localSSRC,
			NTPSec:  sec,
			NTPFrac: frac,
			// The media clock is anchored to the wall clock at the MOST
			// RECENT send, so RTPTime maps the same instant as the NTP pair
			// while staying consistent with the timestamps actually on the
			// wire even after a pacer re-base skips packets (RFC 3550
			// §6.4.1's NTP↔RTP correspondence).
			RTPTime:     m.lastTxTS + durationToTicks(now.Sub(m.lastTxAt), m.cfg.Codec.ClockRate),
			PacketCount: uint32(m.txPkts),
			OctetCount:  uint32(m.txOctets),
			Reports:     blocks,
		}
	} else {
		first = &rtcp.ReceiverReport{SSRC: m.localSSRC, Reports: blocks}
	}
	pkts := []rtcp.Packet{first, rtcp.NewCNAME(m.localSSRC, m.cname)}
	if m.latched && m.scoring {
		pkts = append(pkts, &rtcp.XR{SSRC: m.localSSRC, Blocks: []rtcp.XRBlock{m.voipMetricsLocked()}})
	}
	if final {
		pkts = append(pkts, &rtcp.Bye{SSRCs: []uint32{m.localSSRC}, Reason: "teardown"})
	}
	return pkts
}

// voipMetricsLocked fills the RFC 3611 §4.7 VoIP metrics block from the
// session's cumulative view: loss/discard rates, the Gilbert burst/gap
// measures, delays, the fixed jitter-buffer geometry, and the current
// E-model R and MOS-CQ (×10 on the wire; 127 when unscorable). Analog-plane
// fields (signal/noise/RERL) are Unavailable — loom does not measure them.
// Callers hold mu.
func (m *MediaSession) voipMetricsLocked() *rtcp.XRVoIPMetrics {
	received, _, expected, _ := m.stats.Counts() // allocation-free: this runs on every RTCP send
	var lossFrac, discFrac float64
	if expected > 0 {
		if lost := int64(expected) - int64(received); lost > 0 {
			lossFrac = float64(lost) / float64(expected)
		}
		discFrac = float64(m.discards) / float64(expected)
	}
	gm := m.gil.Metrics()
	jbMs := u16ms(m.jb)
	vm := &rtcp.XRVoIPMetrics{
		SSRC:           m.remoteSSRC,
		LossRate:       u8frac(lossFrac),
		DiscardRate:    u8frac(discFrac),
		BurstDensity:   u8frac(gm.BurstDensity),
		GapDensity:     u8frac(gm.GapDensity),
		BurstDuration:  u16ms(gm.BurstDuration),
		GapDuration:    u16ms(gm.GapDuration),
		RoundTripDelay: u16ms(m.rtt),
		EndSystemDelay: u16ms(m.jb + m.ptime + m.cfg.Codec.FrameLookahead),
		SignalLevel:    rtcp.Unavailable,
		NoiseLevel:     rtcp.Unavailable,
		RERL:           rtcp.Unavailable,
		Gmin:           gilbert.DefaultGmin,
		RFactor:        rtcp.Unavailable,
		ExtRFactor:     rtcp.Unavailable,
		MOSLQ:          rtcp.Unavailable, // listening quality is not modeled separately
		MOSCQ:          rtcp.Unavailable,
		RXConfig:       rxConfigJBFixed,
		JBNominal:      jbMs,
		JBMaximum:      jbMs,
		JBAbsMax:       jbMs,
	}
	if res, ok := m.scoreLocked((lossFrac+discFrac)*100, gm.BurstR); ok {
		// RFC 3611 §4.7: the R factor field is an integer in 0..100 with 127
		// meaning unavailable; anything else MUST NOT be sent. A negative R
		// (terrible but fully measured call) clamps to the legal 0; a
		// wideband-scale R above 100 (G.107.1 runs to 129) has no legal
		// encoding in this narrowband-scaled field, so it stays Unavailable.
		switch r := math.Round(res.R); {
		case r < 0:
			vm.RFactor = 0
		case r <= 100:
			vm.RFactor = uint8(r)
		}
		vm.MOSCQ = uint8(math.Min(50, math.Max(10, math.Round(res.MOSCQ*10))))
	}
	return vm
}

// scoreLocked runs the E-model on the current delay/burst state with the
// given Ppl (percent, clamped to the model's domain). Callers hold mu.
func (m *MediaSession) scoreLocked(ppl, burstR float64) (emodel.Result, bool) {
	if ppl < 0 {
		ppl = 0
	}
	if ppl > 100 {
		ppl = 100
	}
	netOWD := time.Duration(0)
	if est := m.owdLocked(); est.Valid && est.Value > 0 {
		netOWD = est.Value
	}
	ta := emodel.ComposeTa(netOWD, m.jb, m.cfg.Codec)
	res, err := emodel.Score(emodel.Config{Codec: m.cfg.Codec}, emodel.Input{Ta: ta, Ppl: ppl, BurstR: burstR})
	if err != nil {
		return emodel.Result{}, false
	}
	return res, true
}

// owdLocked resolves the one-way-delay tier (design §5): the SR+offset
// measurement when an OffsetProvider produced one, the labeled RTT/2 guess
// when only an RTT exists, else invalid. Callers hold mu.
func (m *MediaSession) owdLocked() owd.Estimate {
	if m.owdEst.Valid {
		return m.owdEst
	}
	if m.haveRTT {
		return owd.Estimate{Value: m.rtt / 2, ErrBound: m.rtt / 2, Method: owd.RTTHalf, Valid: true}
	}
	return owd.Estimate{}
}

// handleRTCP processes one inbound compound: SR NTP anchoring for LSR/DLSR
// and one-way delay, RTT from report blocks echoing our SRs, the remote
// quality view from XR VoIP metrics, and BYE. Once the session has latched,
// compounds from any other source address are dropped and counted as strays
// — the RTCP mirror of the RTP latch rule — and SR/XR state is only adopted
// from the latched SSRC (XR blocks additionally have to describe our own
// stream). An unlatched answerer adopts the first valid RTCP source as its
// provisional return address, so an RTP-silent (recvonly) caller still
// receives media and reports; the RTP latch overrides that address when
// media arrives.
func (m *MediaSession) handleRTCP(b []byte, arrival time.Time, from net.Addr) {
	pkts, err := rtcp.ParseCompound(b)
	if err != nil {
		m.mu.Lock()
		m.strays++
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.latched && from.String() != m.remoteKey {
		m.strays++
		return
	}
	m.signalInbound()
	if !m.caller && m.remoteAddr == nil {
		m.remoteAddr = from
		m.startTxLocked()
	}
	for _, p := range pkts {
		switch t := p.(type) {
		case *rtcp.SenderReport:
			if m.latched && t.SSRC != m.remoteSSRC {
				continue // not the latched peer's stream clock
			}
			m.lastSRCompact = rtcp.CompactNTP(t.NTPSec, t.NTPFrac)
			m.lastSRArrival = arrival
			if m.owdProv != nil {
				if off, eb, ok := m.owdProv.Offset(); ok {
					// offset = remote − local, so the SR's send instant on
					// our clock is srTime − offset and
					// OWD = arrival − (srTime − offset).
					srTime := ntpTime(t.NTPSec, t.NTPFrac)
					m.owdEst = owd.Estimate{
						Value:    arrival.Sub(srTime) + off,
						ErrBound: eb,
						Method:   owd.Synced,
						Valid:    true,
					}
				}
			}
			m.rttFromBlocksLocked(t.Reports, arrival)
		case *rtcp.ReceiverReport:
			m.rttFromBlocksLocked(t.Reports, arrival)
		case *rtcp.XR:
			if m.latched && t.SSRC != m.remoteSSRC {
				continue
			}
			for _, blk := range t.Blocks {
				vm, ok := blk.(*rtcp.XRVoIPMetrics)
				if !ok || vm.SSRC != m.localSSRC {
					continue // the block does not describe our stream
				}
				// RFC 3611 §4.7: R factors outside 0..100 (other than the
				// 127 sentinel) and MOS values outside 10..50 MUST be
				// ignored by the receiving system.
				if vm.RFactor <= 100 {
					m.remoteR = float64(vm.RFactor)
				}
				if vm.MOSCQ >= 10 && vm.MOSCQ <= 50 {
					m.remoteMOS = float64(vm.MOSCQ) / 10
				}
			}
		case *rtcp.Bye:
			m.remoteBye = true
		}
	}
}

// rttFromBlocksLocked extracts the LSR/DLSR round trip from report blocks
// that describe our own stream. Callers hold mu.
func (m *MediaSession) rttFromBlocksLocked(blocks []rtcp.ReportBlock, arrival time.Time) {
	for _, rb := range blocks {
		if rb.SSRC != m.localSSRC {
			continue
		}
		if d, ok := rtcp.RTTFromReport(arrival, rb); ok {
			m.rtt = d
			m.haveRTT = true
		}
	}
}

// ntpTime converts a 64-bit NTP timestamp back to wall time (inverse of
// rtcp.NTPNow; valid until the 2036 era rollover, like the rest of RTCP's
// absolute-time handling).
func ntpTime(sec, frac uint32) time.Time {
	return time.Unix(int64(sec)-ntpEpochOffset, int64(uint64(frac)*1_000_000_000>>32))
}

// u8frac encodes a fraction as RFC 3611 1/256 fixed point, saturating.
func u8frac(f float64) uint8 {
	if f <= 0 {
		return 0
	}
	v := math.Round(f * 256)
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// u16ms encodes a duration as whole milliseconds, saturating at the field.
func u16ms(d time.Duration) uint16 {
	if d <= 0 {
		return 0
	}
	ms := d.Milliseconds()
	if ms > math.MaxUint16 {
		return math.MaxUint16
	}
	return uint16(ms)
}
