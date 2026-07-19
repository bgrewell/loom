// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package app_test

import (
	"strings"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/app"
)

func TestParamsGetString(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    map[string]string
		key  string
		def  string
		want string
	}{
		{"present", map[string]string{"codec": "opus"}, "codec", "pcmu", "opus"},
		{"absent", map[string]string{}, "codec", "pcmu", "pcmu"},
		{"empty value", map[string]string{"codec": ""}, "codec", "pcmu", "pcmu"},
		{"nil map", nil, "codec", "pcmu", "pcmu"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := app.NewParams(tc.m)
			if got := p.GetString(tc.key, tc.def); got != tc.want {
				t.Errorf("GetString(%q, %q) = %q, want %q", tc.key, tc.def, got, tc.want)
			}
			if err := p.Err(); err != nil {
				t.Errorf("Err() = %v, want nil (GetString never errors)", err)
			}
		})
	}
}

func TestParamsGetInt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		m       map[string]string
		key     string
		def     int
		want    int
		wantErr bool
	}{
		{"present", map[string]string{"jb_ms": "40"}, "jb_ms", 20, 40, false},
		{"negative", map[string]string{"n": "-3"}, "n", 0, -3, false},
		{"absent", map[string]string{}, "jb_ms", 20, 20, false},
		{"empty value", map[string]string{"jb_ms": ""}, "jb_ms", 20, 20, false},
		{"malformed", map[string]string{"jb_ms": "forty"}, "jb_ms", 20, 20, true},
		{"float rejected", map[string]string{"jb_ms": "4.5"}, "jb_ms", 20, 20, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := app.NewParams(tc.m)
			if got := p.GetInt(tc.key, tc.def); got != tc.want {
				t.Errorf("GetInt(%q, %d) = %d, want %d", tc.key, tc.def, got, tc.want)
			}
			if gotErr := p.Err() != nil; gotErr != tc.wantErr {
				t.Errorf("Err() = %v, wantErr %v", p.Err(), tc.wantErr)
			}
		})
	}
}

func TestParamsGetDuration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		m       map[string]string
		key     string
		def     time.Duration
		want    time.Duration
		wantErr bool
	}{
		{"present", map[string]string{"ptime": "20ms"}, "ptime", 10 * time.Millisecond, 20 * time.Millisecond, false},
		{"compound", map[string]string{"d": "1m30s"}, "d", 0, 90 * time.Second, false},
		{"absent", map[string]string{}, "ptime", 10 * time.Millisecond, 10 * time.Millisecond, false},
		{"empty value", map[string]string{"ptime": ""}, "ptime", 10 * time.Millisecond, 10 * time.Millisecond, false},
		{"malformed", map[string]string{"ptime": "20"}, "ptime", 10 * time.Millisecond, 10 * time.Millisecond, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := app.NewParams(tc.m)
			if got := p.GetDuration(tc.key, tc.def); got != tc.want {
				t.Errorf("GetDuration(%q, %v) = %v, want %v", tc.key, tc.def, got, tc.want)
			}
			if gotErr := p.Err() != nil; gotErr != tc.wantErr {
				t.Errorf("Err() = %v, wantErr %v", p.Err(), tc.wantErr)
			}
		})
	}
}

// TestParamsErrorAccumulation is the point of the reader: every malformed key
// is reported at once, each matchable in the joined error, and good keys still
// return their values.
func TestParamsErrorAccumulation(t *testing.T) {
	p := app.NewParams(map[string]string{
		"jb_ms": "forty",
		"ptime": "bogus",
		"codec": "pcmu",
	})
	if got := p.GetInt("jb_ms", 20); got != 20 {
		t.Errorf("GetInt fell back to %d, want 20", got)
	}
	if got := p.GetDuration("ptime", 10*time.Millisecond); got != 10*time.Millisecond {
		t.Errorf("GetDuration fell back to %v, want 10ms", got)
	}
	if got := p.GetString("codec", ""); got != "pcmu" {
		t.Errorf("GetString = %q, want pcmu", got)
	}
	err := p.Err()
	if err == nil {
		t.Fatal("Err() = nil, want both parse failures")
	}
	for _, key := range []string{`"jb_ms"`, `"ptime"`} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("Err() = %q, missing param %s", err, key)
		}
	}
}
