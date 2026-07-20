// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package codec

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestSeededRows pins the identity, sizing, delay, and G.113 parameters of
// every seeded codec at the default 20 ms ptime — the contractual values from
// RFC 3551 §4.5/§6, RFC 7587 §4.1, and G.113 Appendix I.
func TestSeededRows(t *testing.T) {
	tests := []struct {
		name        string
		pt          uint8
		clock       uint32
		channels    uint8
		bytes20     int
		samples20   uint32
		lookahead   time.Duration
		ie, bpl     float64
		wideband    bool
		ieWB, bplWB float64
	}{
		{"pcmu", 0, 8000, 1, 160, 160, 250 * time.Microsecond, 0, 25.1, false, 0, 0},
		{"pcma", 8, 8000, 1, 160, 160, 250 * time.Microsecond, 0, 25.1, false, 0, 0},
		{"g729", 18, 8000, 1, 20, 160, 5 * time.Millisecond, 11, 19.0, false, 0, 0},
		{"opus", 111, 48000, 2, 80, 960, 6500 * time.Microsecond, 5, 15, true, 5, 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := ByName(tt.name)
			if err != nil {
				t.Fatalf("ByName(%q): %v", tt.name, err)
			}
			if c.Name != tt.name {
				t.Errorf("Name = %q, want %q", c.Name, tt.name)
			}
			if c.PayloadType != tt.pt {
				t.Errorf("PayloadType = %d, want %d", c.PayloadType, tt.pt)
			}
			if c.ClockRate != tt.clock {
				t.Errorf("ClockRate = %d, want %d", c.ClockRate, tt.clock)
			}
			if c.Channels != tt.channels {
				t.Errorf("Channels = %d, want %d", c.Channels, tt.channels)
			}
			if c.Ptime != 20*time.Millisecond {
				t.Errorf("Ptime = %v, want 20ms", c.Ptime)
			}
			if got := c.PayloadBytes(20 * time.Millisecond); got != tt.bytes20 {
				t.Errorf("PayloadBytes(20ms) = %d, want %d", got, tt.bytes20)
			}
			if got := c.SamplesPerPacket(20 * time.Millisecond); got != tt.samples20 {
				t.Errorf("SamplesPerPacket(20ms) = %d, want %d", got, tt.samples20)
			}
			if c.FrameLookahead != tt.lookahead {
				t.Errorf("FrameLookahead = %v, want %v", c.FrameLookahead, tt.lookahead)
			}
			if c.Ie != tt.ie || c.Bpl != tt.bpl {
				t.Errorf("Ie/Bpl = %v/%v, want %v/%v", c.Ie, c.Bpl, tt.ie, tt.bpl)
			}
			if c.Wideband != tt.wideband {
				t.Errorf("Wideband = %v, want %v", c.Wideband, tt.wideband)
			}
			if c.IeWB != tt.ieWB || c.BplWB != tt.bplWB {
				t.Errorf("IeWB/BplWB = %v/%v, want %v/%v", c.IeWB, c.BplWB, tt.ieWB, tt.bplWB)
			}
		})
	}
}

// TestSizingAcrossPtimes pins payload sizing and timestamp advance at
// non-default ptimes, including the g729 whole-frame rule and the zero/
// negative-ptime guards.
func TestSizingAcrossPtimes(t *testing.T) {
	tests := []struct {
		codec   string
		ptime   time.Duration
		bytes   int
		samples uint32
	}{
		{"pcmu", 10 * time.Millisecond, 80, 80},
		{"pcmu", 30 * time.Millisecond, 240, 240},
		{"pcmu", 0, 0, 0},
		{"pcmu", -20 * time.Millisecond, 0, 0},
		{"g729", 10 * time.Millisecond, 10, 80},
		{"g729", 30 * time.Millisecond, 30, 240},
		{"g729", 15 * time.Millisecond, 10, 120}, // only whole 10 ms frames count
		{"g729", 5 * time.Millisecond, 0, 40},    // sub-frame ptime: no payload
		{"g729", 0, 0, 0},
		{"opus", 10 * time.Millisecond, 40, 480},
		{"opus", 40 * time.Millisecond, 160, 1920},
		{"opus", 0, 0, 0},
	}
	for _, tt := range tests {
		c, err := ByName(tt.codec)
		if err != nil {
			t.Fatalf("ByName(%q): %v", tt.codec, err)
		}
		if got := c.PayloadBytes(tt.ptime); got != tt.bytes {
			t.Errorf("%s.PayloadBytes(%v) = %d, want %d", tt.codec, tt.ptime, got, tt.bytes)
		}
		if got := c.SamplesPerPacket(tt.ptime); got != tt.samples {
			t.Errorf("%s.SamplesPerPacket(%v) = %d, want %d", tt.codec, tt.ptime, got, tt.samples)
		}
	}
}

// TestG711BplConstants pins the PLC-dependent Bpl pair and that the seeded
// G.711 rows default to the PLC-on value.
func TestG711BplConstants(t *testing.T) {
	if G711BplPLC != 25.1 || G711BplNoPLC != 4.3 {
		t.Fatalf("G711 Bpl constants = %v/%v, want 25.1/4.3", G711BplPLC, G711BplNoPLC)
	}
	for _, name := range []string{"pcmu", "pcma"} {
		c, err := ByName(name)
		if err != nil {
			t.Fatalf("ByName(%q): %v", name, err)
		}
		if c.Bpl != G711BplPLC {
			t.Errorf("%s.Bpl = %v, want PLC default %v", name, c.Bpl, G711BplPLC)
		}
	}
}

// TestByNameCaseInsensitive pins that lookup normalizes case.
func TestByNameCaseInsensitive(t *testing.T) {
	for _, name := range []string{"PCMU", "Opus", "pCmA"} {
		if _, err := ByName(name); err != nil {
			t.Errorf("ByName(%q): %v", name, err)
		}
	}
}

// TestByNameUnknown pins the unknown-codec error: it wraps ErrUnknown, names
// the offender, and lists the registered rows.
func TestByNameUnknown(t *testing.T) {
	_, err := ByName("ilbc")
	if err == nil {
		t.Fatal("ByName(\"ilbc\") succeeded, want error")
	}
	if !errors.Is(err, ErrUnknown) {
		t.Errorf("error %v does not wrap ErrUnknown", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, `"ilbc"`) {
		t.Errorf("error %q does not name the unknown codec", msg)
	}
	for _, want := range []string{"pcmu", "pcma", "g729", "opus"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not list registered codec %q", msg, want)
		}
	}
}

// TestRegisterOverride pins the override semantics: Register replaces an
// existing row (the calibration path, e.g. G.711 without PLC), and the
// original can be restored the same way.
func TestRegisterOverride(t *testing.T) {
	orig, err := ByName("pcmu")
	if err != nil {
		t.Fatalf("ByName(pcmu): %v", err)
	}
	defer Register(orig) // restore for other tests

	noPLC := orig
	noPLC.Bpl = G711BplNoPLC
	Register(noPLC)

	got, err := ByName("pcmu")
	if err != nil {
		t.Fatalf("ByName after override: %v", err)
	}
	if got.Bpl != G711BplNoPLC {
		t.Errorf("overridden pcmu.Bpl = %v, want %v", got.Bpl, G711BplNoPLC)
	}
	// The rest of the row is untouched.
	if got.PayloadType != orig.PayloadType || got.ClockRate != orig.ClockRate {
		t.Errorf("override changed PT/clock: %d/%d", got.PayloadType, got.ClockRate)
	}

	Register(orig)
	restored, err := ByName("pcmu")
	if err != nil {
		t.Fatalf("ByName after restore: %v", err)
	}
	if restored.Bpl != G711BplPLC {
		t.Errorf("restored pcmu.Bpl = %v, want %v", restored.Bpl, G711BplPLC)
	}
}

// TestRegisterNew pins that a new row registers under its lower-cased name and
// that a zero Ptime is defaulted to DefaultPtime.
func TestRegisterNew(t *testing.T) {
	Register(Codec{
		Name:             "TestWB",
		PayloadType:      96,
		ClockRate:        16000,
		Channels:         1,
		PayloadBytes:     func(p time.Duration) int { return int(samplesAt(16000, p)) },
		SamplesPerPacket: func(p time.Duration) uint32 { return samplesAt(16000, p) },
		Wideband:         true,
	})
	c, err := ByName("testwb")
	if err != nil {
		t.Fatalf("ByName(testwb): %v", err)
	}
	if c.Ptime != DefaultPtime {
		t.Errorf("defaulted Ptime = %v, want %v", c.Ptime, DefaultPtime)
	}
	if got := c.SamplesPerPacket(c.Ptime); got != 320 {
		t.Errorf("SamplesPerPacket(20ms) = %d, want 320", got)
	}
}

// TestRegisterPanics pins that programming errors panic: empty Name and nil
// sizing funcs.
func TestRegisterPanics(t *testing.T) {
	tests := []struct {
		name string
		c    Codec
	}{
		{"empty name", Codec{
			PayloadBytes:     func(time.Duration) int { return 0 },
			SamplesPerPacket: func(time.Duration) uint32 { return 0 },
		}},
		{"nil PayloadBytes", Codec{
			Name:             "bad",
			SamplesPerPacket: func(time.Duration) uint32 { return 0 },
		}},
		{"nil SamplesPerPacket", Codec{
			Name:         "bad",
			PayloadBytes: func(time.Duration) int { return 0 },
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("Register did not panic")
				}
			}()
			Register(tt.c)
		})
	}
}
