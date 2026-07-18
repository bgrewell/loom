// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package rtp

import (
	"bytes"
	"errors"
	"testing"
)

// TestHeaderRoundTrip pins that MarshalTo/ParseHeader are inverses across
// flag combinations, boundary field values, and CSRC lists, and that the
// marshalled length is 12 + 4·len(CSRC).
func TestHeaderRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		h    Header
	}{
		{"zero", Header{}},
		{"marker", Header{Marker: true, PayloadType: 0}},
		{"padding", Header{Padding: true, PayloadType: 8}},
		{"max fields", Header{
			Marker:         true,
			PayloadType:    127,
			SequenceNumber: 65535,
			Timestamp:      0xFFFFFFFF,
			SSRC:           0xFFFFFFFF,
		}},
		{"one csrc", Header{PayloadType: 18, CSRC: []uint32{0xDEADBEEF}}},
		{"fifteen csrc", Header{CSRC: make([]uint32, 15)}},
		{"typical voice", Header{
			Marker:         true,
			PayloadType:    0,
			SequenceNumber: 4711,
			Timestamp:      160000,
			SSRC:           0x600DCA11,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := make([]byte, HeaderLen+4*len(tt.h.CSRC)+8)
			n, err := tt.h.MarshalTo(buf)
			if err != nil {
				t.Fatalf("MarshalTo: %v", err)
			}
			if want := HeaderLen + 4*len(tt.h.CSRC); n != want {
				t.Fatalf("MarshalTo wrote %d bytes, want %d", n, want)
			}
			if buf[0]>>6 != 2 {
				t.Errorf("version bits = %d, want 2", buf[0]>>6)
			}
			got, off, err := ParseHeader(buf[:n])
			if err != nil {
				t.Fatalf("ParseHeader: %v", err)
			}
			if off != n {
				t.Errorf("payloadOffset = %d, want %d", off, n)
			}
			if got.Padding != tt.h.Padding || got.Extension != tt.h.Extension || got.Marker != tt.h.Marker {
				t.Errorf("flags = %v/%v/%v, want %v/%v/%v",
					got.Padding, got.Extension, got.Marker, tt.h.Padding, tt.h.Extension, tt.h.Marker)
			}
			if got.PayloadType != tt.h.PayloadType {
				t.Errorf("PayloadType = %d, want %d", got.PayloadType, tt.h.PayloadType)
			}
			if got.SequenceNumber != tt.h.SequenceNumber {
				t.Errorf("SequenceNumber = %d, want %d", got.SequenceNumber, tt.h.SequenceNumber)
			}
			if got.Timestamp != tt.h.Timestamp {
				t.Errorf("Timestamp = %d, want %d", got.Timestamp, tt.h.Timestamp)
			}
			if got.SSRC != tt.h.SSRC {
				t.Errorf("SSRC = %#x, want %#x", got.SSRC, tt.h.SSRC)
			}
			if len(got.CSRC) != len(tt.h.CSRC) {
				t.Fatalf("len(CSRC) = %d, want %d", len(got.CSRC), len(tt.h.CSRC))
			}
			for i := range got.CSRC {
				if got.CSRC[i] != tt.h.CSRC[i] {
					t.Errorf("CSRC[%d] = %#x, want %#x", i, got.CSRC[i], tt.h.CSRC[i])
				}
			}
		})
	}
}

// TestHeaderMarshalErrors pins MarshalTo's rejection of short buffers,
// oversized CSRC lists, and 8-bit payload types.
func TestHeaderMarshalErrors(t *testing.T) {
	tests := []struct {
		name    string
		h       Header
		buf     int
		wantErr error // nil means any non-nil error
	}{
		{"buffer one short", Header{}, HeaderLen - 1, ErrShortBuffer},
		{"empty buffer", Header{}, 0, ErrShortBuffer},
		{"csrc needs room", Header{CSRC: []uint32{1}}, HeaderLen, ErrShortBuffer},
		{"sixteen csrc", Header{CSRC: make([]uint32, 16)}, 128, nil},
		{"payload type 128", Header{PayloadType: 128}, 64, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.h.MarshalTo(make([]byte, tt.buf))
			if err == nil {
				t.Fatal("MarshalTo succeeded, want error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want errors.Is %v", err, tt.wantErr)
			}
		})
	}
}

// TestParseHeaderErrors pins the rejection of short buffers, non-2 versions,
// and truncated CSRC/extension regions.
func TestParseHeaderErrors(t *testing.T) {
	valid := make([]byte, HeaderLen)
	h := Header{PayloadType: 0}
	if _, err := h.MarshalTo(valid); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		b       []byte
		wantErr error
	}{
		{"empty", nil, ErrShortBuffer},
		{"eleven bytes", valid[:11], ErrShortBuffer},
		{"version 0", make([]byte, HeaderLen), ErrVersion},
		{"version 1", append([]byte{1 << 6}, valid[1:]...), ErrVersion},
		{"version 3", append([]byte{3 << 6}, valid[1:]...), ErrVersion},
		{"truncated csrc", append([]byte{2<<6 | 2}, valid[1:]...), ErrShortBuffer},
		{"extension flag no header", append([]byte{2<<6 | 1<<4}, valid[1:]...), ErrShortBuffer},
		{
			"extension length overruns",
			// X set, 4-byte extension header declaring 1 word that isn't there.
			append(append([]byte{2<<6 | 1<<4}, valid[1:]...), 0, 0, 0, 1),
			ErrShortBuffer,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ParseHeader(tt.b)
			if err == nil {
				t.Fatal("ParseHeader succeeded, want error")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want errors.Is %v", err, tt.wantErr)
			}
		})
	}
}

// TestParseHeaderExtensionSkip pins that payloadOffset lands past a
// well-formed RFC 3550 §5.3.1 extension header.
func TestParseHeaderExtensionSkip(t *testing.T) {
	b := make([]byte, HeaderLen+4+8+2) // header + ext header + 2 ext words + 2 payload bytes
	h := Header{Extension: true, PayloadType: 96, SequenceNumber: 7, Timestamp: 8, SSRC: 9}
	if _, err := h.MarshalTo(b); err != nil {
		t.Fatal(err)
	}
	// Extension: profile 0xBEDE, length 2 words.
	b[HeaderLen], b[HeaderLen+1] = 0xBE, 0xDE
	b[HeaderLen+3] = 2
	b[len(b)-2], b[len(b)-1] = 0xAB, 0xCD

	got, off, err := ParseHeader(b)
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	if want := HeaderLen + 4 + 8; off != want {
		t.Fatalf("payloadOffset = %d, want %d", off, want)
	}
	if !got.Extension {
		t.Error("Extension flag not parsed")
	}
	if b[off] != 0xAB || b[off+1] != 0xCD {
		t.Errorf("payload at offset = %#x %#x, want 0xAB 0xCD", b[off], b[off+1])
	}
}

// FuzzParseHeader is the header round-trip fuzz: ParseHeader must never
// panic, a successful parse must yield a payload offset inside the buffer,
// and (absent an extension) re-marshalling must reproduce the input bytes.
func FuzzParseHeader(f *testing.F) {
	seed := func(h Header, extra ...byte) []byte {
		b := make([]byte, HeaderLen+4*len(h.CSRC))
		if _, err := h.MarshalTo(b); err != nil {
			f.Fatal(err)
		}
		return append(b, extra...)
	}
	f.Add(seed(Header{Marker: true, PayloadType: 0, SequenceNumber: 1, Timestamp: 2, SSRC: 3}))
	f.Add(seed(Header{Padding: true, PayloadType: 127, CSRC: []uint32{1, 2, 3}}))
	f.Add(seed(Header{PayloadType: 8}, 0xAA, 0xBB))
	f.Add([]byte{1 << 6, 0, 0, 0})                              // wrong version
	f.Add([]byte{2<<6 | 1<<4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}) // X bit, truncated
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, b []byte) {
		h, off, err := ParseHeader(b)
		if err != nil {
			return
		}
		if off < HeaderLen || off > len(b) {
			t.Fatalf("payloadOffset %d outside [12, %d]", off, len(b))
		}
		if h.Extension {
			return // marshal writes the flag but not the extension body
		}
		out := make([]byte, HeaderLen+4*len(h.CSRC))
		n, err := h.MarshalTo(out)
		if err != nil {
			t.Fatalf("re-marshal of parsed header: %v", err)
		}
		if n != off {
			t.Fatalf("re-marshal length %d, parse offset %d", n, off)
		}
		if !bytes.Equal(out, b[:n]) {
			t.Fatalf("round trip mismatch:\n got %x\nwant %x", out, b[:n])
		}
	})
}
