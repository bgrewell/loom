// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/bgrewell/loom/core/units"
)

// Params reads typed values out of an Options.Params map with per-key defaults
// and error accumulation: each accessor returns the parsed value, or the
// default when the key is absent, empty, or malformed. Parse failures are
// recorded rather than returned, so an app factory reads every knob straight
// through and reports all bad parameters at once via Err — one round trip for
// the user instead of one error per rerun.
type Params struct {
	m    map[string]string
	errs []error
}

// NewParams wraps m (typically Options.Params; nil is an empty map) for typed
// access.
func NewParams(m map[string]string) *Params {
	return &Params{m: m}
}

// GetString returns the value for key, or def when the key is absent or empty.
func (p *Params) GetString(key, def string) string {
	if v, ok := p.m[key]; ok && v != "" {
		return v
	}
	return def
}

// GetInt returns the integer value for key, or def when the key is absent or
// empty. A malformed value records an error and returns def.
func (p *Params) GetInt(key string, def int) int {
	v, ok := p.m[key]
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("param %q: %w", key, err))
		return def
	}
	return n
}

// GetDuration returns the duration value for key (Go duration grammar, e.g.
// "20ms", "1m30s"), or def when the key is absent or empty. A malformed value
// records an error and returns def.
func (p *Params) GetDuration(key string, def time.Duration) time.Duration {
	v, ok := p.m[key]
	if !ok || v == "" {
		return def
	}
	d, err := units.ParseDuration(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("param %q: %w", key, err))
		return def
	}
	return d
}

// Err returns all accumulated parse errors joined (matched individually via
// errors.Is/As), or nil if every accessed parameter parsed cleanly. Factories
// call it once after reading their knobs.
func (p *Params) Err() error {
	return errors.Join(p.errs...)
}
