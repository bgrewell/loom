// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package selection

import (
	"math/rand"
	"sort"
	"testing"

	"github.com/bgrewell/loom/core/scenario"
)

func endpoints() []scenario.Endpoint {
	return []scenario.Endpoint{
		{Name: "client-a", Tags: []string{"vm", "10g", "linux"}},
		{Name: "client-b", Tags: []string{"vm", "10g", "linux"}},
		{Name: "edge", Tags: []string{"server", "40g", "win"}},
	}
}

func names(es []scenario.Endpoint) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Name
	}
	sort.Strings(out)
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestResolve(t *testing.T) {
	es := endpoints()
	cases := []struct {
		sel  scenario.Selector
		want []string
	}{
		{scenario.Selector{Raw: "edge"}, []string{"edge"}},
		{scenario.Selector{Raw: "all"}, []string{"client-a", "client-b", "edge"}},
		{scenario.Selector{Raw: "tags(all(10g, linux))"}, []string{"client-a", "client-b"}},
		{scenario.Selector{Raw: "tags(any(40g, 10g))"}, []string{"client-a", "client-b", "edge"}},
		{scenario.Selector{Raw: "tags(not(win))"}, []string{"client-a", "client-b"}},
		{scenario.Selector{Raw: "tags(all(10g, not(win)))"}, []string{"client-a", "client-b"}},
		{scenario.Selector{Mode: "oneOf", List: []string{"client-a", "edge"}}, []string{"client-a", "edge"}},
	}
	for _, c := range cases {
		got, err := Resolve(c.sel, es)
		if err != nil {
			t.Fatalf("Resolve(%+v): %v", c.sel, err)
		}
		if !eq(names(got), c.want) {
			t.Fatalf("Resolve(%+v) = %v, want %v", c.sel, names(got), c.want)
		}
	}
}

func TestResolveErrors(t *testing.T) {
	es := endpoints()
	if _, err := Resolve(scenario.Selector{Raw: "ghost"}, es); err == nil {
		t.Error("unknown name should error")
	}
	if _, err := Resolve(scenario.Selector{Raw: "tags(bogus(x))"}, es); err == nil {
		t.Error("unknown tag function should error")
	}
}

func TestPickExcludes(t *testing.T) {
	pool := endpoints()
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 50; i++ {
		got, ok := Pick(pool, "edge", r)
		if !ok || got.Name == "edge" {
			t.Fatalf("Pick returned %q (ok=%v), should never be the excluded edge", got.Name, ok)
		}
	}
	// excluding the only candidate → not ok
	if _, ok := Pick([]scenario.Endpoint{{Name: "solo"}}, "solo", r); ok {
		t.Fatal("excluding the only endpoint should yield ok=false")
	}
}
