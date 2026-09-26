package state

import (
	"fmt"
	"strings"
	"time"
)

// StatePatch exposes only agent-proposable progress. RunSpec, World, and runtime
// metadata cannot be expressed here. Every key/ID may occur in one operation only.
type StatePatch struct {
	AddMemory []MemoryEntry `json:"add_memory,omitempty"`
	// UpsertMemory replaces the entire entry at Key, or appends it if absent.
	UpsertMemory []MemoryEntry  `json:"upsert_memory,omitempty"`
	RemoveMemory []string       `json:"remove_memory,omitempty"`
	AddIntent    []Intent       `json:"add_intent,omitempty"`
	UpdateIntent []IntentUpdate `json:"update_intent,omitempty"`
	// Removal is explicit and permitted for any existing intent, including cancelled
	// or irrelevant work. It does not execute or cancel an external action.
	RemoveIntent []string `json:"remove_intent,omitempty"`
}

type IntentUpdate struct {
	ID         string    `json:"id"`
	Executed   bool      `json:"executed"`
	ExecutedAt time.Time `json:"executed_at,omitempty"`
}

func (p StatePatch) Empty() bool {
	return len(p.AddMemory) == 0 && len(p.UpsertMemory) == 0 && len(p.RemoveMemory) == 0 && len(p.AddIntent) == 0 && len(p.UpdateIntent) == 0 && len(p.RemoveIntent) == 0
}

// ValidatePatch checks all operations and the final memory bounds before any
// mutation. Removals of different keys can make room in the same atomic patch.
func ValidatePatch(current AgentState, p StatePatch) error {
	memories := make(map[string]MemoryEntry, len(current.WorkingMemory))
	for _, m := range current.WorkingMemory {
		if _, exists := memories[m.Key]; exists {
			return fmt.Errorf("duplicate stored memory key")
		}
		memories[m.Key] = m
	}
	touched := make(map[string]bool)
	for _, m := range p.AddMemory {
		if err := validateMemoryEntry(m); err != nil {
			return err
		}
		if _, exists := memories[m.Key]; exists || touched[m.Key] {
			return fmt.Errorf("duplicate memory key %q", m.Key)
		}
		touched[m.Key] = true
		memories[m.Key] = m
	}
	for _, m := range p.UpsertMemory {
		if err := validateMemoryEntry(m); err != nil {
			return err
		}
		if touched[m.Key] {
			return fmt.Errorf("conflicting memory key %q", m.Key)
		}
		touched[m.Key] = true
		memories[m.Key] = m
	}
	for _, key := range p.RemoveMemory {
		if _, exists := memories[key]; !exists || touched[key] {
			return fmt.Errorf("unknown or conflicting memory key %q", key)
		}
		touched[key] = true
		delete(memories, key)
	}
	if len(memories) > MaxWorkingMemoryEntries {
		return fmt.Errorf("working memory exceeds %d entries", MaxWorkingMemoryEntries)
	}
	// Iterate in state order for deterministic validation errors.
	for _, old := range current.WorkingMemory {
		if m, exists := memories[old.Key]; exists {
			if err := validateMemoryEntry(m); err != nil {
				return err
			}
		}
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
	for _, id := range p.RemoveIntent {
		if !intents[id] || touched[id] {
			return fmt.Errorf("unknown or conflicting intent id %q", id)
		}
		touched[id] = true
	}
	return nil
}

// ApplyPatch is atomic and leaves the input snapshot untouched on all paths.
func ApplyPatch(current AgentState, p StatePatch) (AgentState, error) {
	if err := ValidatePatch(current, p); err != nil {
		return current.Clone(), err
	}
	next := current.Clone()
	next.WorkingMemory = append(next.WorkingMemory, p.AddMemory...)
	indexes := make(map[string]int, len(next.WorkingMemory))
	for i, m := range next.WorkingMemory {
		indexes[m.Key] = i
	}
	for _, m := range p.UpsertMemory {
		if i, exists := indexes[m.Key]; exists {
			next.WorkingMemory[i] = m
		} else {
			indexes[m.Key] = len(next.WorkingMemory)
			next.WorkingMemory = append(next.WorkingMemory, m)
		}
	}
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
	clear(next.WorkingMemory[len(kept):])
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
	remove = make(map[string]bool)
	for _, id := range p.RemoveIntent {
		remove[id] = true
	}
	keptIntents := next.PendingIntents[:0]
	for _, intent := range next.PendingIntents {
		if !remove[intent.ID] {
			keptIntents = append(keptIntents, intent)
		}
	}
	clear(next.PendingIntents[len(keptIntents):])
	next.PendingIntents = keptIntents
	return next, nil
}
