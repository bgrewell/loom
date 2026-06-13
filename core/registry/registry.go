// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package registry provides a generic, concurrency-safe name→factory registry.
// Each component kind (datapath, scheduler, generator, payload) keeps its own
// registry so new backends and protocols register themselves and drop in
// without a central switch. See DESIGN.md §5.
package registry

import (
	"fmt"
	"sort"
	"sync"
)

// Factory builds a T from its options O.
type Factory[T any, O any] func(O) (T, error)

// Registry maps names to factories for one component kind. T is the produced
// interface; O is that kind's options struct.
type Registry[T any, O any] struct {
	mu sync.RWMutex
	f  map[string]Factory[T, O]
}

// New returns an empty Registry.
func New[T any, O any]() *Registry[T, O] {
	return &Registry[T, O]{f: make(map[string]Factory[T, O])}
}

// Register adds a factory under name. It panics on a nil factory or a duplicate
// name, since registration happens at init time and both are programming errors.
func (r *Registry[T, O]) Register(name string, fn Factory[T, O]) {
	if fn == nil {
		panic("registry: nil factory for " + name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.f[name]; dup {
		panic("registry: duplicate registration for " + name)
	}
	r.f[name] = fn
}

// Build constructs the named component from opts, or returns an error naming the
// registered alternatives.
func (r *Registry[T, O]) Build(name string, opts O) (T, error) {
	r.mu.RLock()
	fn, ok := r.f[name]
	r.mu.RUnlock()
	if !ok {
		var zero T
		return zero, fmt.Errorf("registry: unknown name %q (have %v)", name, r.Names())
	}
	return fn(opts)
}

// Names returns the registered names, sorted.
func (r *Registry[T, O]) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.f))
	for n := range r.f {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
