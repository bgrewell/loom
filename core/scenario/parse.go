// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"bytes"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Parse decodes and validates a scenario from YAML.
func Parse(data []byte) (*Scenario, error) {
	var s Scenario
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&s); err != nil {
		return nil, fmt.Errorf("scenario: %w", err)
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *Scenario) validate() error {
	if s.Name == "" {
		return errors.New("scenario: name (scenario:) is required")
	}
	if len(s.Timeline) == 0 {
		return errors.New("scenario: timeline has no events")
	}
	seen := make(map[string]struct{}, len(s.Timeline))
	for i, ev := range s.Timeline {
		if ev.Name == "" {
			return fmt.Errorf("scenario: timeline[%d] has no name", i)
		}
		if _, dup := seen[ev.Name]; dup {
			return fmt.Errorf("scenario: duplicate event name %q", ev.Name)
		}
		seen[ev.Name] = struct{}{}
		if ev.Flow.Kind == "" {
			return fmt.Errorf("scenario: event %q has no flow kind", ev.Name)
		}
	}
	return nil
}
