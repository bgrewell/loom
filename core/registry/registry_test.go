// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"strconv"
	"testing"
)

func TestBuildAndNames(t *testing.T) {
	r := New[string, int]()
	r.Register("double", func(n int) (string, error) { return strconv.Itoa(n * 2), nil })
	r.Register("triple", func(n int) (string, error) { return strconv.Itoa(n * 3), nil })

	got, err := r.Build("double", 21)
	if err != nil || got != "42" {
		t.Fatalf("Build(double,21) = %q, %v", got, err)
	}

	if _, err := r.Build("missing", 0); err == nil {
		t.Fatal("Build of unknown name should error")
	}

	names := r.Names()
	if len(names) != 2 || names[0] != "double" || names[1] != "triple" {
		t.Fatalf("Names = %v, want sorted [double triple]", names)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate registration should panic")
		}
	}()
	r := New[int, int]()
	r.Register("x", func(int) (int, error) { return 0, nil })
	r.Register("x", func(int) (int, error) { return 0, nil })
}

func TestRegisterNilPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("nil factory should panic")
		}
	}()
	New[int, int]().Register("x", nil)
}
