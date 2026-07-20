// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package rtp

// This file is the package's wire-realism acceptance test: an internal,
// test-only pcap writer that deterministically produces
// testdata/g711-call.pcap — one second of G.711 μ-law RTP inside
// Ethernet+IPv4+UDP, with correct IP and UDP checksums — plus a re-parse
// test that walks the capture back through ParseHeader and ReceiverStats.
// The file is committed as the golden capture for manual Wireshark
// verification (RTP stream analysis must show 0 lost / 0 jitter and the
// audio must play); regeneration is byte-for-byte deterministic.

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	goldenPcapPath = "testdata/g711-call.pcap"

	pcapPackets = 50         // one second at 20 ms ptime
	pcapBaseSec = 1767225600 // 2026-01-01T00:00:00Z
	pcapSSRC    = 0x600DCA11
	// The identity is chosen so the capture also exercises both 16-bit
	// sequence wrap (65531 + 50 crosses 65535) and 32-bit timestamp wrap.
	pcapSeq0 = uint16(65531)
	pcapTS0  = uint32(0xFFFFFF00)

	pcapSrcPort = 40000
	pcapDstPort = 40002
)

var (
	pcapSrcIP  = []byte{192, 0, 2, 10}
	pcapDstIP  = []byte{192, 0, 2, 20}
	pcapSrcMAC = []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	pcapDstMAC = []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x02}
)

// onesSum accumulates 16-bit big-endian words for the Internet checksum
// (RFC 1071), padding an odd trailing byte with zero.
func onesSum(b []byte, sum uint32) uint32 {
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	return sum
}

func foldSum(sum uint32) uint16 {
	for sum>>16 != 0 {
		sum = sum&0xFFFF + sum>>16
	}
	return uint16(sum)
}

func ipChecksum(hdr []byte) uint16 { return ^foldSum(onesSum(hdr, 0)) }

// udpChecksum computes the UDP checksum over the IPv4 pseudo-header and the
// UDP segment (whose checksum field must be zero); an all-zero result is
// transmitted as 0xFFFF per RFC 768.
func udpChecksum(src, dst, udp []byte) uint16 {
	sum := onesSum(src, 0)
	sum = onesSum(dst, sum)
	sum += 17 // protocol
	sum += uint32(len(udp))
	sum = onesSum(udp, sum)
	cs := ^foldSum(sum)
	if cs == 0 {
		cs = 0xFFFF
	}
	return cs
}

// g711CallFrame builds Ethernet+IPv4+UDP+RTP frame i of the golden call.
func g711CallFrame(t *testing.T, source PayloadSource, i int) []byte {
	t.Helper()
	payload := make([]byte, 160)
	if n := source.Fill(payload, uint64(i)); n != 160 {
		t.Fatalf("Fill = %d, want 160", n)
	}
	h := Header{
		Marker:         i == 0, // first packet of the talkspurt
		PayloadType:    0,      // PCMU
		SequenceNumber: pcapSeq0 + uint16(i),
		Timestamp:      pcapTS0 + uint32(160*i),
		SSRC:           pcapSSRC,
	}
	rtpPkt := make([]byte, HeaderLen+len(payload))
	hn, err := h.MarshalTo(rtpPkt)
	if err != nil {
		t.Fatal(err)
	}
	copy(rtpPkt[hn:], payload)

	udp := make([]byte, 8+len(rtpPkt))
	binary.BigEndian.PutUint16(udp[0:], pcapSrcPort)
	binary.BigEndian.PutUint16(udp[2:], pcapDstPort)
	binary.BigEndian.PutUint16(udp[4:], uint16(len(udp)))
	copy(udp[8:], rtpPkt)
	binary.BigEndian.PutUint16(udp[6:], udpChecksum(pcapSrcIP, pcapDstIP, udp))

	ip := make([]byte, 20)
	ip[0] = 0x45 // IPv4, 20-byte header
	ip[1] = 0xB8 // DSCP EF: what a real voice bearer marks
	binary.BigEndian.PutUint16(ip[2:], uint16(20+len(udp)))
	binary.BigEndian.PutUint16(ip[4:], uint16(0x4000+i)) // ID
	binary.BigEndian.PutUint16(ip[6:], 0x4000)           // DF
	ip[8] = 64                                           // TTL
	ip[9] = 17                                           // UDP
	copy(ip[12:], pcapSrcIP)
	copy(ip[16:], pcapDstIP)
	binary.BigEndian.PutUint16(ip[10:], ipChecksum(ip))

	frame := make([]byte, 0, 14+len(ip)+len(udp))
	frame = append(frame, pcapDstMAC...)
	frame = append(frame, pcapSrcMAC...)
	frame = append(frame, 0x08, 0x00) // EtherType IPv4
	frame = append(frame, ip...)
	frame = append(frame, udp...)
	return frame
}

// buildG711CallPcap renders the full deterministic capture: classic pcap
// (magic 0xA1B2C3D4, microsecond timestamps, LINKTYPE_ETHERNET).
func buildG711CallPcap(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w16 := func(v uint16) { _ = binary.Write(&buf, binary.LittleEndian, v) }
	w32 := func(v uint32) { _ = binary.Write(&buf, binary.LittleEndian, v) }

	w32(0xA1B2C3D4) // magic
	w16(2)          // version major
	w16(4)          // version minor
	w32(0)          // thiszone
	w32(0)          // sigfigs
	w32(65535)      // snaplen
	w32(1)          // LINKTYPE_ETHERNET

	source := NewG711Source("mulaw")
	for i := 0; i < pcapPackets; i++ {
		frame := g711CallFrame(t, source, i)
		w32(pcapBaseSec)
		w32(uint32(i * 20000)) // 20 ms per packet, microseconds
		w32(uint32(len(frame)))
		w32(uint32(len(frame)))
		buf.Write(frame)
	}
	return buf.Bytes()
}

// TestGoldenPcapFile keeps testdata/g711-call.pcap byte-for-byte identical
// to the deterministic generator (creating it on first run so it can be
// committed as the Wireshark golden capture).
func TestGoldenPcapFile(t *testing.T) {
	want := buildG711CallPcap(t)
	got, err := os.ReadFile(goldenPcapPath)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(goldenPcapPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPcapPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes); commit it as the golden capture", goldenPcapPath, len(want))
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the deterministic generator; delete it and rerun to regenerate", goldenPcapPath)
	}
}

// TestGoldenPcapReparse walks the generated capture back through the
// package: link/IP/UDP framing and checksums verify, every RTP header
// parses with the expected identity, payload bytes match a fresh G.711
// source, and ReceiverStats fed with the capture's own timestamps reports a
// clean stream — 49 counted packets (probation eats the first), zero loss,
// zero jitter (perfect 20 ms pacing survives both the sequence wrap and the
// timestamp wrap) — the same numbers Wireshark's RTP stream analysis must
// show.
func TestGoldenPcapReparse(t *testing.T) {
	data := buildG711CallPcap(t)
	if binary.LittleEndian.Uint32(data) != 0xA1B2C3D4 {
		t.Fatal("bad pcap magic")
	}
	stats := NewReceiverStats(8000)
	source := NewG711Source("mulaw")
	want := make([]byte, 160)

	off := 24
	count := 0
	for off < len(data) {
		sec := binary.LittleEndian.Uint32(data[off:])
		usec := binary.LittleEndian.Uint32(data[off+4:])
		capLen := int(binary.LittleEndian.Uint32(data[off+8:]))
		frame := data[off+16 : off+16+capLen]
		off += 16 + capLen

		if want := 14 + 20 + 8 + 12 + 160; capLen != want {
			t.Fatalf("packet %d: frame is %d bytes, want %d", count, capLen, want)
		}
		if frame[12] != 0x08 || frame[13] != 0x00 {
			t.Fatalf("packet %d: EtherType %#x%02x, want IPv4", count, frame[12], frame[13])
		}
		ip := frame[14:34]
		if ip[9] != 17 {
			t.Fatalf("packet %d: IP protocol %d, want UDP", count, ip[9])
		}
		if foldSum(onesSum(ip, 0)) != 0xFFFF {
			t.Errorf("packet %d: IP header checksum does not verify", count)
		}
		udp := frame[34:]
		if got := binary.BigEndian.Uint16(udp[0:]); got != pcapSrcPort {
			t.Errorf("packet %d: src port %d, want %d", count, got, pcapSrcPort)
		}
		if got := binary.BigEndian.Uint16(udp[2:]); got != pcapDstPort {
			t.Errorf("packet %d: dst port %d, want %d", count, got, pcapDstPort)
		}
		sum := onesSum(pcapSrcIP, 0)
		sum = onesSum(pcapDstIP, sum)
		sum += 17 + uint32(len(udp))
		if foldSum(onesSum(udp, sum)) != 0xFFFF {
			t.Errorf("packet %d: UDP checksum does not verify", count)
		}

		h, po, err := ParseHeader(udp[8:])
		if err != nil {
			t.Fatalf("packet %d: ParseHeader: %v", count, err)
		}
		if h.PayloadType != 0 || h.SSRC != pcapSSRC {
			t.Errorf("packet %d: PT=%d SSRC=%#x, want 0/%#x", count, h.PayloadType, h.SSRC, uint32(pcapSSRC))
		}
		if h.Marker != (count == 0) {
			t.Errorf("packet %d: Marker = %v", count, h.Marker)
		}
		if wantSeq := pcapSeq0 + uint16(count); h.SequenceNumber != wantSeq {
			t.Errorf("packet %d: seq %d, want %d", count, h.SequenceNumber, wantSeq)
		}
		if wantTS := pcapTS0 + uint32(160*count); h.Timestamp != wantTS {
			t.Errorf("packet %d: ts %d, want %d", count, h.Timestamp, wantTS)
		}
		payload := udp[8+po:]
		source.Fill(want, uint64(count))
		if !bytes.Equal(payload, want) {
			t.Errorf("packet %d: payload does not match the deterministic source", count)
		}

		stats.Observe(h, len(payload), time.Unix(int64(sec), int64(usec)*1000))
		count++
	}
	if count != pcapPackets {
		t.Fatalf("parsed %d packets, want %d", count, pcapPackets)
	}

	c := stats.Cumulative()
	if c.Received != pcapPackets-1 || c.Expected != pcapPackets-1 {
		t.Errorf("Received=%d Expected=%d, want %d/%d (probation consumes the first packet)",
			c.Received, c.Expected, pcapPackets-1, pcapPackets-1)
	}
	if c.CumulativeLost != 0 || c.Duplicates != 0 || c.Reordered != 0 {
		t.Errorf("Lost=%d Dup=%d Reorder=%d, want 0/0/0", c.CumulativeLost, c.Duplicates, c.Reordered)
	}
	if c.JitterTicks != 0 || c.JitterMs != 0 {
		t.Errorf("JitterTicks=%d JitterMs=%v, want 0/0 for perfect pacing", c.JitterTicks, c.JitterMs)
	}
	if wantExt := uint32(65536 + 44); c.ExtHighestSeq != wantExt {
		t.Errorf("ExtHighestSeq = %d, want %d (sequence wrapped once)", c.ExtHighestSeq, wantExt)
	}
	if c.MaxGap != 20*time.Millisecond {
		t.Errorf("MaxGap = %v, want 20ms", c.MaxGap)
	}
	if len(c.MediaGaps) != 0 {
		t.Errorf("MediaGaps = %d, want 0", len(c.MediaGaps))
	}
	r := stats.Report()
	if r.FractionLost != 0 || r.CumulativeLost != 0 || r.Jitter != 0 {
		t.Errorf("Report = %+v, want clean", r)
	}
	if r.ExtHighestSeq != uint32(65536+44) {
		t.Errorf("Report().ExtHighestSeq = %d, want %d", r.ExtHighestSeq, 65536+44)
	}
}
