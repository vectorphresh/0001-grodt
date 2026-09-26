package state

import (
	"fmt"
	"strings"
	"time"
)

// StatePatch exposes only agent-proposable fields. Runtime metadata and World
// cannot be expressed here. Patch application never changes revision metadata.
type StatePatch struct {
	Goal         *string        `json:"goal,omitempty"`
	AddMemory    []MemoryEntry  `json:"add_memory,omitempty"`
	RemoveMemory []string       `json:"remove_memory,omitempty"`
	AddIntent    []Intent       `json:"add_intent,omitempty"`
	UpdateIntent []IntentUpdate `json:"update_intent,omitempty"`
}

type IntentUpdate struct {
	ID         string    `json:"id"`
	Executed   bool      `json:"executed"`
	ExecutedAt time.Time `json:"executed_at,omitempty"`
}

func (p StatePatch) Empty() bool {
	return p.Goal == nil && len(p.AddMemory) == 0 && len(p.RemoveMemory) == 0 && len(p.AddIntent) == 0 && len(p.UpdateIntent) == 0
}

// ValidatePatch checks identifiers against the current snapshot. Conflicting
// operations are rejected rather than relying on implicit operation ordering.
func ValidatePatch(current AgentState, p StatePatch) error {
	memories := make(map[string]bool)
	for _, m := range current.WorkingMemory {
		memories[m.Key] = true
	}
	touched := make(map[string]bool)
	for _, m := range p.AddMemory {
		if strings.TrimSpace(m.Key) == "" || strings.TrimSpace(m.Value) == "" {
			return fmt.Errorf("memory key and value are required")
		}
		if memories[m.Key] || touched[m.Key] {
			return fmt.Errorf("duplicate memory key %q", m.Key)
		}
		touched[m.Key] = true
	}
	for _, key := range p.RemoveMemory {
		if !memories[key] || touched[key] {
			return fmt.Errorf("unknown or conflicting memory key %q", key)
		}
		touched[key] = true
	}
	intents := make(map[string]bool)
	for _, i := range current.PendingIntents {
		intents[i.ID] = true
	}
	touched = make(map[string]bool)
	for _, i := range p.AddIntent {
		if strings.TrimSpace(i.ID) == "" || strings.TrimSpace(i.Action) == "" {
			return fmt.Errorf("intent id and action are required")
		}
		if intents[i.ID] || touched[i.ID] {
			return fmt.Errorf("duplicate intent id %q", i.ID)
		}
		if i.Executed != !i.ExecutedAt.IsZero() {
			return fmt.Errorf("intent execution status and timestamp must agree")
		}
		touched[i.ID] = true
	}
	for _, i := range p.UpdateIntent {
		if !intents[i.ID] || touched[i.ID] {
			return fmt.Errorf("unknown or conflicting intent id %q", i.ID)
		}
		if i.Executed != !i.ExecutedAt.IsZero() {
			return fmt.Errorf("intent execution status and timestamp must agree")
		}
		touched[i.ID] = true
	}
	return nil
}

// ApplyPatch is atomic and leaves the input snapshot untouched on all paths.
func ApplyPatch(current AgentState, p StatePatch) (AgentState, error) {
	if err := ValidatePatch(current, p); err != nil {
		return current.Clone(), err
	}
	next := current.Clone()
	if p.Goal != nil {
		next.Goal = *p.Goal
	}
	next.WorkingMemory = append(next.WorkingMemory, p.AddMemory...)
	remove := make(map[string]bool)
	for _, key := range p.RemoveMemory {
		remove[key] = true
	}
	kept := next.WorkingMemory[:0]
	for _, m := range next.WorkingMemory {
		if !remove[m.Key] {
			kept = append(kept, m)
		}
	}
	next.WorkingMemory = kept
	next.PendingIntents = append(next.PendingIntents, p.AddIntent...)
	for _, update := range p.UpdateIntent {
		for i := range next.PendingIntents {
			if next.PendingIntents[i].ID == update.ID {
				next.PendingIntents[i].Executed = update.Executed
				next.PendingIntents[i].ExecutedAt = update.ExecutedAt
			}
		}
	}
	return next, nil
}
