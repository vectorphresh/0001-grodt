package state

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRestoreKnowledgeSeedsValidatedValues(t *testing.T) {
	s := newStore(t,
		Definition{"a", json.RawMessage(`{"type":"integer"}`), json.RawMessage(`1`), &processor{fn: func(json.RawMessage, Event) (json.RawMessage, error) { return json.RawMessage(`{"status":"ignored"}`), nil }}},
		Definition{"b", json.RawMessage(`{"type":"integer"}`), json.RawMessage(`2`), &processor{fn: func(json.RawMessage, Event) (json.RawMessage, error) { return json.RawMessage(`{"status":"ignored"}`), nil }}},
	)
	if err := s.RestoreKnowledge(context.Background(), map[string]json.RawMessage{"a": json.RawMessage(`42`)}); err != nil {
		t.Fatal(err)
	}
	partition := s.Snapshot().Knowledge["a"]
	if string(partition.Value) != "42" || partition.Metadata.Version != 1 || partition.Metadata.Stale {
		t.Fatalf("restore did not seed value: %+v", partition)
	}
	if string(s.Snapshot().Knowledge["b"].Value) != "2" {
		t.Fatal("unrelated partition changed")
	}
}

func TestRestoreKnowledgeRejectsAtomically(t *testing.T) {
	s := newStore(t,
		Definition{"a", json.RawMessage(`{"type":"integer"}`), json.RawMessage(`1`), &processor{fn: func(json.RawMessage, Event) (json.RawMessage, error) { return json.RawMessage(`{"status":"ignored"}`), nil }}},
	)
	for _, values := range []map[string]json.RawMessage{
		{"unknown": json.RawMessage(`1`)},
		{"a": json.RawMessage(`"not-an-integer"`)},
		{"a": json.RawMessage(`1`), "unknown": json.RawMessage(`1`)},
		{"a": json.RawMessage(`{`)},
	} {
		if err := s.RestoreKnowledge(context.Background(), values); err == nil {
			t.Fatalf("restore accepted %v", values)
		}
		if string(s.Snapshot().Knowledge["a"].Value) != "1" {
			t.Fatalf("rejected restore mutated partition: %s", s.Snapshot().Knowledge["a"].Value)
		}
	}
}
