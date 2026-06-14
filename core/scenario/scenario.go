// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package scenario is the parsed model of a loom scenario file
// (docs/scenario-schema.md): endpoints, defaults, and a timeline of events with
// the value grammar (via core/units). Selector/Start/Stop resolution lives in
// later packages; this is the data model + parser.
package scenario

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/bgrewell/loom/core/units"
)

// Scenario is a whole scenario file.
type Scenario struct {
	Name        string         `yaml:"scenario"`
	Description string         `yaml:"description"`
	Seed        int64          `yaml:"seed"`
	Defaults    Defaults       `yaml:"defaults"`
	Endpoints   []Endpoint     `yaml:"endpoints"`
	Timeline    []Event        `yaml:"timeline"`
	Report      map[string]any `yaml:"report"`
}

// Defaults are applied to every event unless overridden.
type Defaults struct {
	Datapath  string         `yaml:"datapath"`
	Scheduler map[string]any `yaml:"scheduler"`
}

// Endpoint is a named point traffic runs between.
type Endpoint struct {
	Name    string   `yaml:"name"`
	Tags    []string `yaml:"tags"`
	Address string   `yaml:"address"`
}

// Event is one timeline entry.
type Event struct {
	Name     string   `yaml:"name"`
	Flow     Flow     `yaml:"flow"`
	From     Selector `yaml:"from"`
	To       Selector `yaml:"to"`
	Datapath string   `yaml:"datapath"`
	Start    Start    `yaml:"start"`
	Repeat   *Repeat  `yaml:"repeat"`
	Stop     Stop     `yaml:"stop"`
	Count    int      `yaml:"count"`
}

// Flow is the {kind, ...params} flow descriptor; params stay opaque.
type Flow struct {
	Kind   string
	Params map[string]any
}

// UnmarshalYAML splits `kind` from the remaining params.
func (f *Flow) UnmarshalYAML(n *yaml.Node) error {
	var m map[string]any
	if err := n.Decode(&m); err != nil {
		return err
	}
	f.Params = make(map[string]any)
	for k, v := range m {
		if k == "kind" {
			f.Kind, _ = v.(string)
		} else {
			f.Params[k] = v
		}
	}
	if f.Kind == "" {
		return fmt.Errorf("flow: missing kind")
	}
	return nil
}

// Selector is an endpoint selector: a scalar (a name, "any", "all", or a tag
// expression like "tags(all(10g,linux))") or a mode map ({oneOf|allOf|any: [...]}).
// Resolution is done by the endpoint-selection package.
type Selector struct {
	Raw  string   // scalar form
	Mode string   // "oneOf"|"allOf"|"any" for the map form
	List []string // members for the map form
}

// UnmarshalYAML accepts the scalar or mode-map form.
func (s *Selector) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		s.Raw = n.Value
		return nil
	}
	var m map[string][]string
	if err := n.Decode(&m); err != nil {
		return fmt.Errorf("selector: %w", err)
	}
	// Exactly one mode key. Ranging over a multi-key map would keep an arbitrary
	// entry (Go map order is randomized), making scenarios non-reproducible.
	if len(m) != 1 {
		return fmt.Errorf("selector: expected exactly one mode (oneOf/allOf/any), got %d", len(m))
	}
	for k, v := range m {
		s.Mode, s.List = k, v
	}
	return nil
}

// Start is when an event first fires: a relative offset ("0s", "+45s") or an
// absolute wall-clock ({at: "12:00:00"}).
type Start struct {
	Offset   time.Duration
	Absolute string
}

// UnmarshalYAML accepts the offset or {at:} form.
func (s *Start) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		d, err := units.ParseDuration(strings.TrimPrefix(n.Value, "+"))
		if err != nil {
			return fmt.Errorf("start %q: %w", n.Value, err)
		}
		s.Offset = d
		return nil
	}
	var m map[string]string
	if err := n.Decode(&m); err != nil {
		return err
	}
	s.Absolute = m["at"]
	return nil
}

// Repeat makes an event recurring.
type Repeat struct {
	Interval string `yaml:"interval"` // value-grammar range, e.g. "10ms..100ms"
	Jitter   string `yaml:"jitter"`   // sampling mode (default uniform)
	Count    int    `yaml:"count"`    // optional cap on fires
	Until    string `yaml:"until"`    // optional stop offset, e.g. "+60s"
}

// Stop bounds an event instance: "end-of-test", or {after|volume|count}.
type Stop struct {
	EndOfTest bool
	After     time.Duration
	Volume    uint64
	Count     uint64
}

// UnmarshalYAML accepts the keyword or map form.
func (s *Stop) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		if n.Value == "end-of-test" {
			s.EndOfTest = true
			return nil
		}
		return fmt.Errorf("stop %q: only 'end-of-test' is valid as a scalar", n.Value)
	}
	var m map[string]any
	if err := n.Decode(&m); err != nil {
		return err
	}
	if v, ok := m["after"]; ok {
		d, err := units.ParseDuration(fmt.Sprint(v))
		if err != nil {
			return err
		}
		s.After = d
	}
	if v, ok := m["volume"]; ok {
		b, err := units.ParseSize(fmt.Sprint(v))
		if err != nil {
			return err
		}
		s.Volume = b
	}
	if v, ok := m["count"]; ok {
		c, err := strconv.ParseUint(fmt.Sprint(v), 10, 64)
		if err != nil {
			return fmt.Errorf("stop count %v: %w", v, err)
		}
		s.Count = c
	}
	return nil
}
