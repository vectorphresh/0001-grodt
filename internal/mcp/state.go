// internal/mcp/state.go
package mcp

import (
	"errors"
	"strings"
	"time"
)

type State struct {
	Objectives []string `json:"objectives"`
	Memory     []Memory `json:"memory"`
	Strategy   string   `json:"strategy"`
}

func (s State) Validate(now time.Time) error {
	if s.Objectives == nil {
		return errors.New("objectives must not be null")
	}

	if s.Memory == nil {
		return errors.New("memory must not be null")
	}

	for i, objective := range s.Objectives {
		if strings.TrimSpace(objective) == "" {
			return errors.New("objective must not be empty")
		}

		_ = i // useful once error includes path/index
	}

	for _, memory := range s.Memory {
		if strings.TrimSpace(memory.Key) == "" {
			return errors.New("memory key must not be empty")
		}

		if strings.TrimSpace(memory.Value) == "" {
			return errors.New("memory value must not be empty")
		}

		if memory.ObservedAt.IsZero() {
			return errors.New("memory observed_at must be set")
		}

		if memory.ObservedAt.After(now) {
			return errors.New("memory observed_at cannot be in the future")
		}
	}

	return nil
}
