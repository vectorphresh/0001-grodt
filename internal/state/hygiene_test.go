package state_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/state"
)

func sizedMemory(t *testing.T, size int) state.MemoryEntry {
	t.Helper()
	entry := state.MemoryEntry{Key: "sized"}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	entry.Value = strings.Repeat("x", size-len(encoded))
	encoded, err = json.Marshal(entry)
	if err != nil || len(encoded) != size {
		t.Fatalf("entry size: %d, %v", len(encoded), err)
	}
	return entry
}

func TestMemoryUpsertReplacesWholeEntryAndCreatesMissing(t *testing.T) {
	original := sampleState()
	before := original.Clone()
	replacement := state.MemoryEntry{Key: "remember", Value: "corrected", ObservedAt: original.UpdatedAt}
	next, err := state.ApplyPatch(original, state.StatePatch{UpsertMemory: []state.MemoryEntry{replacement, {Key: "new", Value: "new fact"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.WorkingMemory) != 2 || next.WorkingMemory[0] != replacement || next.WorkingMemory[1].Key != "new" {
		t.Fatalf("upsert: %+v", next.WorkingMemory)
	}
	next.WorkingMemory[0].Value = "changed by caller"
	next.PendingIntents[0].Action = "changed by caller"
	next.World.Balances[0].Amount = "changed by caller"
	if !reflect.DeepEqual(original, before) {
		t.Fatal("upsert retained input aliases")
	}
	replacement.ObservedAt = time.Time{}
	cleared, err := state.ApplyPatch(before, state.StatePatch{UpsertMemory: []state.MemoryEntry{replacement}})
	if err != nil || cleared.WorkingMemory[0] != replacement {
		t.Fatalf("whole entry replacement: %+v, %v", cleared, err)
	}
}

func TestMemoryBounds(t *testing.T) {
	full := state.AgentState{}
	for i := 0; i < state.MaxWorkingMemoryEntries; i++ {
		full.WorkingMemory = append(full.WorkingMemory, state.MemoryEntry{Key: fmt.Sprintf("key-%d", i), Value: "fact"})
	}
	cases := []struct {
		name    string
		initial state.AgentState
		patch   state.StatePatch
		valid   bool
	}{
		{"exact byte limit", state.AgentState{}, state.StatePatch{AddMemory: []state.MemoryEntry{sizedMemory(t, state.MaxMemoryEntryBytes)}}, true},
		{"above byte limit", state.AgentState{}, state.StatePatch{AddMemory: []state.MemoryEntry{sizedMemory(t, state.MaxMemoryEntryBytes+1)}}, false},
		{"exact count limit", state.AgentState{}, state.StatePatch{AddMemory: full.WorkingMemory}, true},
		{"above count limit", full, state.StatePatch{AddMemory: []state.MemoryEntry{{Key: "extra", Value: "fact"}}}, false},
		{"upsert new above count", full, state.StatePatch{UpsertMemory: []state.MemoryEntry{{Key: "extra", Value: "fact"}}}, false},
		{"upsert existing at count", full, state.StatePatch{UpsertMemory: []state.MemoryEntry{{Key: "key-0", Value: "corrected"}}}, true},
		{"upsert exact byte limit", state.AgentState{WorkingMemory: []state.MemoryEntry{{Key: "sized", Value: "small"}}}, state.StatePatch{UpsertMemory: []state.MemoryEntry{sizedMemory(t, state.MaxMemoryEntryBytes)}}, true},
		{"upsert above byte limit", state.AgentState{WorkingMemory: []state.MemoryEntry{{Key: "sized", Value: "small"}}}, state.StatePatch{UpsertMemory: []state.MemoryEntry{sizedMemory(t, state.MaxMemoryEntryBytes+1)}}, false},
		{"remove makes room", full, state.StatePatch{RemoveMemory: []string{"key-0"}, UpsertMemory: []state.MemoryEntry{{Key: "new", Value: "fact"}}}, true},
		{"valid removal and oversized entry", full, state.StatePatch{RemoveMemory: []string{"key-0"}, UpsertMemory: []state.MemoryEntry{sizedMemory(t, state.MaxMemoryEntryBytes+1)}}, false},
		{"valid removal still exceeds count", full, state.StatePatch{RemoveMemory: []string{"key-0"}, AddMemory: []state.MemoryEntry{{Key: "new-1", Value: "fact"}, {Key: "new-2", Value: "fact"}}}, false},
		{"large key counts", state.AgentState{}, state.StatePatch{AddMemory: []state.MemoryEntry{{Key: strings.Repeat("k", state.MaxMemoryEntryBytes), Value: "v"}}}, false},
		{"escaped JSON counts", state.AgentState{}, state.StatePatch{AddMemory: []state.MemoryEntry{{Key: "k", Value: strings.Repeat("\n", state.MaxMemoryEntryBytes/2)}}}, false},
		{"UTF8 bytes count", state.AgentState{}, state.StatePatch{AddMemory: []state.MemoryEntry{{Key: "k", Value: strings.Repeat("界", state.MaxMemoryEntryBytes/3+1)}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.initial.Clone()
			result, err := state.ApplyPatch(tc.initial, tc.patch)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
			if !reflect.DeepEqual(tc.initial, before) {
				t.Fatal("input mutated")
			}
			if !tc.valid && !reflect.DeepEqual(result, before) {
				t.Fatal("partial patch applied")
			}
			if tc.valid {
				if err := state.ValidateWorkingMemory(result.WorkingMemory); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestHygieneConflictsAreAtomic(t *testing.T) {
	entry := state.MemoryEntry{Key: "remember", Value: "corrected"}
	cases := map[string]state.StatePatch{
		"memory add remove":                   {AddMemory: []state.MemoryEntry{{Key: "new", Value: "x"}}, RemoveMemory: []string{"new"}},
		"memory upsert remove":                {UpsertMemory: []state.MemoryEntry{entry}, RemoveMemory: []string{"remember"}},
		"memory add upsert":                   {AddMemory: []state.MemoryEntry{{Key: "new", Value: "x"}}, UpsertMemory: []state.MemoryEntry{{Key: "new", Value: "y"}}},
		"duplicate upserts":                   {UpsertMemory: []state.MemoryEntry{entry, entry}},
		"duplicate removes":                   {RemoveMemory: []string{"remember", "remember"}},
		"empty upsert key":                    {UpsertMemory: []state.MemoryEntry{{Value: "x"}}},
		"empty upsert value":                  {UpsertMemory: []state.MemoryEntry{{Key: "remember"}}},
		"intent update remove":                {UpdateIntent: []state.IntentUpdate{{ID: "intent-1", Executed: true, ExecutedAt: sampleState().UpdatedAt}}, RemoveIntent: []string{"intent-1"}},
		"intent add remove":                   {AddIntent: []state.Intent{{ID: "new", Action: "inspect"}}, RemoveIntent: []string{"new"}},
		"intent duplicate removes":            {RemoveIntent: []string{"intent-1", "intent-1"}},
		"intent unknown remove":               {RemoveIntent: []string{"missing"}},
		"valid upsert invalid intent removal": {UpsertMemory: []state.MemoryEntry{entry}, RemoveIntent: []string{"missing"}},
	}
	for name, patch := range cases {
		t.Run(name, func(t *testing.T) {
			original := sampleState()
			before := original.Clone()
			result, err := state.ApplyPatch(original, patch)
			if err == nil {
				t.Fatal("conflict accepted")
			}
			if !reflect.DeepEqual(original, before) || !reflect.DeepEqual(result, before) {
				t.Fatal("partial mutation")
			}
		})
	}
}

func TestExplicitIntentRemovalPreservesOtherIntents(t *testing.T) {
	for _, executed := range []bool{false, true} {
		original := sampleState()
		original.PendingIntents[0].Executed = executed
		if executed {
			original.PendingIntents[0].ExecutedAt = original.UpdatedAt
		}
		original.PendingIntents = append(original.PendingIntents, state.Intent{ID: "keep", Action: "still relevant"})
		next, err := state.ApplyPatch(original, state.StatePatch{RemoveIntent: []string{"intent-1"}})
		if err != nil || len(next.PendingIntents) != 1 || next.PendingIntents[0].ID != "keep" {
			t.Fatalf("removal: %+v, %v", next, err)
		}
		if len(original.PendingIntents) != 2 {
			t.Fatal("original intents changed")
		}
	}
}

func TestMemoryStoreRejectsInvalidMemoryWithoutChangingSnapshot(t *testing.T) {
	store := state.NewMemoryStore()
	original := sampleState()
	if err := store.Save(context.Background(), &original); err != nil {
		t.Fatal(err)
	}
	invalid := original.Clone()
	invalid.WorkingMemory = []state.MemoryEntry{sizedMemory(t, state.MaxMemoryEntryBytes+1)}
	if err := store.Save(context.Background(), &invalid); err == nil {
		t.Fatal("invalid memory saved")
	}
	saved, err := store.Load(context.Background())
	if err != nil || !reflect.DeepEqual(*saved, original) {
		t.Fatal("failed save changed stored state")
	}
}
