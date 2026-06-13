// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package timeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/scenario"
)

func testScenario() *scenario.Scenario {
	return &scenario.Scenario{
		Seed: 42,
		Timeline: []scenario.Event{
			{Name: "single", Start: scenario.Start{Offset: 5 * time.Second}},
			{Name: "rep", Start: scenario.Start{Offset: 0},
				Repeat: &scenario.Repeat{Interval: "10ms..20ms", Count: 5}},
		},
	}
}

func counts(fires []Fire) map[string]int {
	m := map[string]int{}
	for _, f := range fires {
		m[f.Event]++
	}
	return m
}

func TestPlanCountsAndOrder(t *testing.T) {
	fires, err := Plan(testScenario(), 10*time.Second)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	c := counts(fires)
	if c["single"] != 1 || c["rep"] != 5 {
		t.Fatalf("counts = %v, want single:1 rep:5", c)
	}
	// sorted by At
	for i := 1; i < len(fires); i++ {
		if fires[i].At < fires[i-1].At {
			t.Fatalf("fires not sorted at %d: %v", i, fires)
		}
	}
	// rep's first fire is at offset 0
	if fires[0].Event != "rep" || fires[0].At != 0 {
		t.Fatalf("first fire = %+v, want rep@0", fires[0])
	}
}

func TestPlanDeterministic(t *testing.T) {
	a, _ := Plan(testScenario(), 10*time.Second)
	b, _ := Plan(testScenario(), 10*time.Second)
	if len(a) != len(b) {
		t.Fatalf("len %d != %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("fire %d differs: %+v vs %+v", i, a[i], b[i])
		}
	}
}

func TestPlanHorizonBounds(t *testing.T) {
	s := &scenario.Scenario{
		Seed: 1,
		Timeline: []scenario.Event{
			{Name: "rep", Repeat: &scenario.Repeat{Interval: "10ms"}}, // scalar = fixed gap
		},
	}
	fires, err := Plan(s, 55*time.Millisecond)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// fires at 0,10,20,30,40,50ms → 6
	if len(fires) != 6 {
		t.Fatalf("got %d fires, want 6: %v", len(fires), fires)
	}
	for _, f := range fires {
		if f.At > 55*time.Millisecond {
			t.Fatalf("fire past horizon: %v", f)
		}
	}
}

func TestRunDrivesFiresInOrder(t *testing.T) {
	fires := []Fire{
		{Event: "a", At: 0},
		{Event: "b", At: 5 * time.Millisecond},
		{Event: "c", At: 10 * time.Millisecond},
	}
	var mu sync.Mutex
	var order []string
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	Run(ctx, fires, time.Now(), func(f Fire) {
		mu.Lock()
		order = append(order, f.Event)
		mu.Unlock()
	})
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 || order[0] != "a" || order[2] != "c" {
		t.Fatalf("fire order = %v", order)
	}
}
