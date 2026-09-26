package state_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/state"
)

func sampleState() state.AgentState {
	return state.AgentState{
		Version: 7, UpdatedAt: time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC),
		World: state.WorldState{
			Balances:     []state.Balance{{Asset: "USD", Amount: "100"}},
			Positions:    []state.Position{{Symbol: "TEST", Quantity: "1"}},
			ActiveOrders: []state.Order{{ID: "order-1", Status: "open"}},
		},
		WorkingMemory:  []state.MemoryEntry{{Key: "remember", Value: "useful"}},
		PendingIntents: []state.Intent{{ID: "intent-1", Action: "inspect"}},
	}
}

func mutate(s *state.AgentState) {
	s.World.Balances[0].Amount = "999"
	s.World.Positions[0].Quantity = "999"
	s.World.ActiveOrders[0].Status = "changed"
	s.WorkingMemory[0].Value = "changed"
	s.PendingIntents[0].Action = "changed"
}

func TestMemoryStoreSnapshotIsolation(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	empty, err := store.Load(ctx)
	if err != nil || empty == nil {
		t.Fatalf("initial load: %v, %v", empty, err)
	}
	original := sampleState()
	want := original.Clone()
	if err := store.Save(ctx, &original); err != nil {
		t.Fatal(err)
	}
	mutate(&original)
	loaded, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*loaded, want) {
		t.Fatal("Save retained aliases")
	}
	mutate(loaded)
	reloaded, _ := store.Load(ctx)
	if !reflect.DeepEqual(*reloaded, want) {
		t.Fatal("Load retained aliases")
	}
	if err := store.Save(ctx, nil); err == nil {
		t.Fatal("nil save accepted")
	}
}

func TestMemoryStoreCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := state.NewMemoryStore()
	if _, err := store.Load(ctx); err != context.Canceled {
		t.Fatalf("load: %v", err)
	}
	if err := store.Save(ctx, &state.AgentState{}); err != context.Canceled {
		t.Fatalf("save: %v", err)
	}
}

func TestTypedPatchPreservesRuntimeFields(t *testing.T) {
	original := sampleState()
	before := original.Clone()
	next, err := state.ApplyPatch(original, state.StatePatch{
		AddMemory: []state.MemoryEntry{{Key: "new", Value: "new conclusion"}}, RemoveMemory: []string{"remember"},
		AddIntent:    []state.Intent{{ID: "intent-2", Action: "next action"}},
		UpdateIntent: []state.IntentUpdate{{ID: "intent-1", Executed: true, ExecutedAt: original.UpdatedAt}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, before) {
		t.Fatal("patch mutated input")
	}
	if !reflect.DeepEqual(next.World, original.World) || next.Version != original.Version || !next.UpdatedAt.Equal(original.UpdatedAt) {
		t.Fatal("patch changed runtime-owned fields")
	}
	if len(next.WorkingMemory) != 1 || next.WorkingMemory[0].Key != "new" || len(next.PendingIntents) != 2 || !next.PendingIntents[0].Executed {
		t.Fatalf("patch result: %+v", next)
	}
}

func TestInvalidPatchesAreAtomic(t *testing.T) {
	cases := map[string]state.StatePatch{
		"empty memory":            {AddMemory: []state.MemoryEntry{{Key: "", Value: "x"}}},
		"duplicate stored memory": {AddMemory: []state.MemoryEntry{{Key: "remember", Value: "x"}}},
		"duplicate new memory":    {AddMemory: []state.MemoryEntry{{Key: "new", Value: "x"}, {Key: "new", Value: "y"}}},
		"unknown memory":          {RemoveMemory: []string{"missing"}},
		"duplicate removal":       {RemoveMemory: []string{"remember", "remember"}},
		"empty intent":            {AddIntent: []state.Intent{{ID: "new"}}},
		"duplicate intent":        {AddIntent: []state.Intent{{ID: "intent-1", Action: "duplicate"}}},
		"unknown update":          {UpdateIntent: []state.IntentUpdate{{ID: "missing"}}},
		"duplicate update":        {UpdateIntent: []state.IntentUpdate{{ID: "intent-1"}, {ID: "intent-1"}}},
		"execution without time":  {UpdateIntent: []state.IntentUpdate{{ID: "intent-1", Executed: true}}},
	}
	for name, patch := range cases {
		t.Run(name, func(t *testing.T) {
			original := sampleState()
			before := original.Clone()
			patch.UpsertMemory = []state.MemoryEntry{{Key: "valid", Value: "must not persist"}}
			returned, err := state.ApplyPatch(original, patch)
			if err == nil {
				t.Fatal("invalid patch accepted")
			}
			if !reflect.DeepEqual(original, before) || !reflect.DeepEqual(returned, before) {
				t.Fatal("partial patch applied")
			}
		})
	}
}
