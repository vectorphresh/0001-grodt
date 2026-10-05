package state

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestModuleOwnedProgressSurvivesCyclesAndInvalidates(t *testing.T) {
	ctx := context.Background()
	var taskID string
	m := &processor{fn: func(_ json.RawMessage, e Event) (json.RawMessage, error) {
		switch string(e.Payload) {
		case `"establish"`:
			return encode(map[string]any{"status": "mutation", "replace": map[string]any{"reference_value": 100, "current_value": 99}, "progress": []ProgressRecord{
				{ID: "reference", TaskID: taskID, Kind: "conclusion", Status: "established", Summary: "Authoritative reference established and fixed", FactReference: "/reference_value"},
				{ID: "inspection", TaskID: taskID, Kind: "completed_step", Status: "established", Summary: "Initial inspection completed"},
				{ID: "next", TaskID: taskID, Kind: "focus", Status: "unresolved", Summary: "Evaluate available candidates"},
			}}), nil
		case `"failure"`:
			return json.RawMessage(`{"status":"error"}`), nil
		case `"change"`:
			return json.RawMessage(`{"status":"mutation","replace":{"reference_value":100,"current_value":98}}`), nil
		default:
			return json.RawMessage(`{"status":"ignored"}`), nil
		}
	}}
	s := newStore(t, Definition{Name: "world", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{}`), Module: m})
	var err error
	taskID, err = s.Push(ctx, "Complete objective", "request")
	if err != nil {
		t.Fatal(err)
	}
	admit := func(payload string) {
		t.Helper()
		if _, err := s.Admit(ctx, Source{Kind: "user", ID: "test"}, json.RawMessage(payload)); err != nil {
			t.Fatal(err)
		}
	}
	admit(`"establish"`)
	before := s.ModelJSON()
	if !bytes.Contains(before, []byte("Initial inspection completed")) || !bytes.Contains(before, []byte("Authoritative reference established and fixed")) || !bytes.Contains(before, []byte(`"reference_value":100`)) || !bytes.Contains(before, []byte(`"active_focus"`)) {
		t.Fatal("knowledge, progress, or focus missing")
	}
	for i := 0; i < 25; i++ {
		if err := s.BeginCycle(ctx); err != nil {
			t.Fatal(err)
		}
		admit(`"noop"`)
	}
	after := s.ModelJSON()
	if len(after) > len(before)+20 {
		t.Fatalf("cycle history grew projection: %d -> %d", len(before), len(after))
	}
	if !bytes.Contains(after, []byte("Initial inspection completed")) {
		t.Fatal("completed step forgotten")
	}
	admit(`"failure"`)
	if !bytes.Contains(s.ModelJSON(), []byte(`"freshness":"stale"`)) || bytes.Contains(s.ModelJSON(), []byte(`"status":"invalidated"`)) {
		t.Fatal("freshness conflated with semantic invalidation")
	}
	if s.Snapshot().Knowledge["world"].Progress[0].Status != "established" {
		t.Fatal("projection modified durable outcome")
	}
	admit(`"establish"`)
	if bytes.Contains(s.ModelJSON(), []byte(`"status":"invalidated"`)) {
		t.Fatal("refreshed progress remains invalid")
	}
	admit(`"change"`)
	if !bytes.Contains(s.ModelJSON(), []byte(`"freshness":"superseded"`)) || bytes.Contains(s.ModelJSON(), []byte(`"status":"invalidated"`)) {
		t.Fatal("version change semantically invalidated historical progress")
	}
	if !bytes.Contains(encode(s.Journal()), []byte("Initial inspection completed")) {
		t.Fatal("progress provenance missing")
	}
	if len(s.Snapshot().Knowledge["world"].Progress) != 3 {
		t.Fatal("progress appended instead of replaced")
	}
}

func TestProgressValidationAndAtomicCommit(t *testing.T) {
	for _, raw := range []string{
		`{"status":"ignored","progress":[]}`,
		`{"status":"processed","progress":null}`,
		`{"status":"processed","progress":[{"id":"x","task_id":"t","kind":"reasoning","status":"established","summary":"bad"}]}`,
		`{"status":"processed","progress":[{"id":"x","task_id":"t","kind":"conclusion","status":"established","summary":"` + strings.Repeat("x", 513) + `"}]}`,
		`{"status":"processed","progress":[{"id":"x","task_id":"t","kind":"conclusion","status":"established","summary":"outcome","raw_payload":{}}]}`,
	} {
		if _, err := decodeResult([]byte(raw)); err == nil {
			t.Fatalf("invalid progress accepted: %.100s", raw)
		}
	}
	m := &processor{fn: func(json.RawMessage, Event) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"mutation","replace":1,"progress":[{"id":"x","task_id":"missing","kind":"conclusion","status":"established","summary":"outcome"}]}`), nil
	}}
	s := newStore(t, Definition{Name: "world", Schema: json.RawMessage(`{"type":"integer"}`), Initial: json.RawMessage(`0`), Module: m})
	if _, err := s.Admit(context.Background(), Source{Kind: "user", ID: "test"}, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if string(s.Snapshot().Knowledge["world"].Value) != "0" || len(s.Snapshot().Knowledge["world"].Progress) != 0 {
		t.Fatal("invalid progress partially committed")
	}
}

func TestModuleDeclaredSemanticInvalidationRemainsAuthoritative(t *testing.T) {
	var taskID string
	m := &processor{fn: func(_ json.RawMessage, _ Event) (json.RawMessage, error) {
		return encode(map[string]any{"status": "processed", "progress": []ProgressRecord{{ID: "baseline", TaskID: taskID, Kind: "conclusion", Status: "invalidated", Summary: "Module explicitly invalidated the conclusion"}}}), nil
	}}
	s := newStore(t, Definition{Name: "world", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{}`), Module: m})
	var err error
	taskID, err = s.Push(context.Background(), "Evaluate", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Admit(context.Background(), Source{Kind: "user", ID: "invalidate"}, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	projection := s.ModelJSON()
	if !bytes.Contains(projection, []byte(`"status":"invalidated"`)) || !bytes.Contains(projection, []byte(`"freshness":"current"`)) {
		t.Fatal("semantic invalidation conflated with freshness")
	}
}
