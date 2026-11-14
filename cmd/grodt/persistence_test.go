package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/state"
)

func TestKnowledgePersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge.json")
	defs := []state.Definition{{Name: "market", Schema: json.RawMessage(`{"type":"object","required":["strategy_context"],"properties":{"strategy_context":{"type":"string"}},"additionalProperties":false}`), Initial: json.RawMessage(`{"strategy_context":""}`), Module: &stateModule{}}}
	store, err := state.New(context.Background(), defs, state.Options{RunID: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RestoreKnowledge(context.Background(), map[string]json.RawMessage{"market": json.RawMessage(`{"strategy_context":"momentum test"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := saveKnowledge(store, path); err != nil {
		t.Fatal(err)
	}
	store.Close(context.Background())

	store2, err := state.New(context.Background(), defs, state.Options{RunID: "second"})
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close(context.Background())
	if err := restoreKnowledge(context.Background(), store2, path); err != nil {
		t.Fatal(err)
	}
	value := store2.Snapshot().Knowledge["market"].Value
	if string(value) != `{"strategy_context":"momentum test"}` {
		t.Fatalf("restored value %s", value)
	}
}

func TestKnowledgePersistenceMissingFileStartsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.json")
	defs := []state.Definition{{Name: "market", Schema: json.RawMessage(`true`), Initial: json.RawMessage(`1`), Module: &stateModule{}}}
	store, err := state.New(context.Background(), defs, state.Options{RunID: "first"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	if err := restoreKnowledge(context.Background(), store, path); err != nil {
		t.Fatal(err)
	}
	if string(store.Snapshot().Knowledge["market"].Value) != "1" {
		t.Fatal("fresh start changed initial value")
	}
}

func TestKnowledgePersistenceCorruptFileFailsStartup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "knowledge.json")
	if err := os.WriteFile(path, []byte(`{"knowledge":`), 0o600); err != nil {
		t.Fatal(err)
	}
	defs := []state.Definition{{Name: "market", Schema: json.RawMessage(`true`), Initial: json.RawMessage(`1`), Module: &stateModule{}}}
	store, err := state.New(context.Background(), defs, state.Options{RunID: "first"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	if err := restoreKnowledge(context.Background(), store, path); err == nil {
		t.Fatal("corrupt persistence file accepted")
	}
}
