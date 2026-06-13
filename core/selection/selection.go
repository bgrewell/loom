// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package selection resolves scenario endpoint selectors against the endpoint
// set: names, oneOf/allOf/any modes, and tag expressions like
// tags(all(10g, not(win))). See docs/scenario-schema.md and DESIGN.md §9.
package selection

import (
	"fmt"
	"math/rand"
	"strings"

	"github.com/bgrewell/loom/core/scenario"
)

// Resolve returns the endpoints a selector matches (the candidate pool). It is
// deterministic; random "pick one" and client != server exclusion are applied
// separately by Pick.
func Resolve(sel scenario.Selector, endpoints []scenario.Endpoint) ([]scenario.Endpoint, error) {
	if sel.Mode != "" {
		return byNames(endpoints, sel.List)
	}
	raw := strings.TrimSpace(sel.Raw)
	switch {
	case raw == "":
		return nil, fmt.Errorf("selection: empty selector")
	case raw == "all", raw == "any":
		return endpoints, nil
	case strings.HasPrefix(raw, "tags(") && strings.HasSuffix(raw, ")"):
		expr, err := parseExpr(raw[len("tags(") : len(raw)-1])
		if err != nil {
			return nil, fmt.Errorf("selection: %w", err)
		}
		var out []scenario.Endpoint
		for _, e := range endpoints {
			if expr.eval(tagSet(e.Tags)) {
				out = append(out, e)
			}
		}
		return out, nil
	default:
		for _, e := range endpoints {
			if e.Name == raw {
				return []scenario.Endpoint{e}, nil
			}
		}
		return nil, fmt.Errorf("selection: unknown endpoint %q", raw)
	}
}

// Pick chooses one endpoint from a pool at random, excluding the named endpoint
// (e.g. to keep client != server). ok is false if the pool is empty after
// exclusion.
func Pick(pool []scenario.Endpoint, exclude string, r *rand.Rand) (scenario.Endpoint, bool) {
	cand := make([]scenario.Endpoint, 0, len(pool))
	for _, e := range pool {
		if e.Name != exclude {
			cand = append(cand, e)
		}
	}
	if len(cand) == 0 {
		return scenario.Endpoint{}, false
	}
	return cand[r.Intn(len(cand))], true
}

func byNames(endpoints []scenario.Endpoint, names []string) ([]scenario.Endpoint, error) {
	idx := make(map[string]scenario.Endpoint, len(endpoints))
	for _, e := range endpoints {
		idx[e.Name] = e
	}
	out := make([]scenario.Endpoint, 0, len(names))
	for _, n := range names {
		e, ok := idx[n]
		if !ok {
			return nil, fmt.Errorf("selection: unknown endpoint %q", n)
		}
		out = append(out, e)
	}
	return out, nil
}

func tagSet(tags []string) map[string]bool {
	m := make(map[string]bool, len(tags))
	for _, t := range tags {
		m[t] = true
	}
	return m
}

// --- tag expression: all(...)/any(...)/not(x)/<tag>, nestable ---

type tagExpr interface{ eval(map[string]bool) bool }

type tagLit string

func (t tagLit) eval(m map[string]bool) bool { return m[string(t)] }

type andExpr []tagExpr

func (e andExpr) eval(m map[string]bool) bool {
	for _, s := range e {
		if !s.eval(m) {
			return false
		}
	}
	return true
}

type orExpr []tagExpr

func (e orExpr) eval(m map[string]bool) bool {
	for _, s := range e {
		if s.eval(m) {
			return true
		}
	}
	return false
}

type notExpr struct{ e tagExpr }

func (n notExpr) eval(m map[string]bool) bool { return !n.e.eval(m) }

func parseExpr(s string) (tagExpr, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty tag expression")
	}
	if i := strings.IndexByte(s, '('); i >= 0 && strings.HasSuffix(s, ")") {
		name := strings.TrimSpace(s[:i])
		args, err := splitArgs(s[i+1 : len(s)-1])
		if err != nil {
			return nil, err
		}
		subs := make([]tagExpr, len(args))
		for j, a := range args {
			if subs[j], err = parseExpr(a); err != nil {
				return nil, err
			}
		}
		switch name {
		case "all":
			return andExpr(subs), nil
		case "any":
			return orExpr(subs), nil
		case "not":
			if len(subs) != 1 {
				return nil, fmt.Errorf("not() takes exactly one argument")
			}
			return notExpr{subs[0]}, nil
		default:
			return nil, fmt.Errorf("unknown tag function %q", name)
		}
	}
	return tagLit(s), nil
}

// splitArgs splits a top-level comma list, respecting nested parentheses.
func splitArgs(s string) ([]string, error) {
	var args []string
	depth, start := 0, 0
	for i, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unbalanced parentheses")
			}
		case ',':
			if depth == 0 {
				args = append(args, s[start:i])
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("unbalanced parentheses")
	}
	args = append(args, s[start:])
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out, nil
}
