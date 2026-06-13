// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package timesync

import (
	"testing"
	"time"
)

func TestOffsetAndDelay(t *testing.T) {
	tests := []struct {
		name                  string
		t1, t2, t3, t4        int64
		wantOffset, wantDelay int64
	}{
		{
			name: "symmetric ahead",
			t1:   0, t2: 10, t3: 12, t4: 20,
			wantOffset: 1, wantDelay: 18,
		},
		{
			name: "perfectly synced",
			t1:   100, t2: 150, t3: 160, t4: 210,
			wantOffset: 0, wantDelay: 100,
		},
		{
			name: "remote behind",
			t1:   0, t2: -5, t3: -3, t4: 10,
			wantOffset: -9, wantDelay: 8,
		},
		{
			name: "zero processing time",
			t1:   1000, t2: 1500, t3: 1500, t4: 2000,
			wantOffset: 0, wantDelay: 1000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Offset(tt.t1, tt.t2, tt.t3, tt.t4); got != tt.wantOffset {
				t.Errorf("Offset = %d, want %d", got, tt.wantOffset)
			}
			if got := Delay(tt.t1, tt.t2, tt.t3, tt.t4); got != tt.wantDelay {
				t.Errorf("Delay = %d, want %d", got, tt.wantDelay)
			}
		})
	}
}

func TestNewSample(t *testing.T) {
	s := NewSample(0, 10, 12, 20)
	if s.Offset != time.Duration(1) {
		t.Errorf("Offset = %v, want 1ns", s.Offset)
	}
	if s.Delay != time.Duration(18) {
		t.Errorf("Delay = %v, want 18ns", s.Delay)
	}
}
