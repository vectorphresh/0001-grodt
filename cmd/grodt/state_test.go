package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/state"
)

type stateModule struct{ events []state.Event }

func (m *stateModule) Process(_ context.Context, _ json.RawMessage, e state.Event) (json.RawMessage, error) {
	m.events = append(m.events, e)
	return json.RawMessage(`{"status":"processed"}`), nil
}
func (m *stateModule) Close(context.Context) error { return nil }
func TestRunStateLifecycleAndAdmission(t *testing.T) {
	for _, scenario := range []string{"corrected", "downstream_rejected", "incomplete"} {
		t.Run(scenario, func(t *testing.T) {
			m := &stateModule{}
			store, err := state.New(context.Background(), []state.Definition{{Name: "sample", Schema: json.RawMessage(`true`), Initial: json.RawMessage(`{}`), Module: m}}, state.Options{RunID: "test"})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close(context.Background())
			calls := 0
			c := testClient{prompt: func(_ context.Context, p string) (openai.TextResult, error) {
				if !strings.Contains(p, `"global_cycle":`) || !strings.Contains(p, `"objective":"goal"`) {
					t.Fatal("missing task/runtime view")
				}
				return openai.TextResult{Text: "answer"}, nil
			}, mutation: func(_ context.Context, i string, s, o json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
				calls++
				if !strings.Contains(i, `"knowledge"`) {
					t.Fatal("missing evaluation state")
				}
				switch scenario {
				case "corrected":
					if calls == 1 {
						return openai.JSONResult{JSON: json.RawMessage(`{}`)}, nil
					}
					return evaluationResult(true, "done"), nil
				case "downstream_rejected":
					return evaluationResult(true, " "), nil
				default:
					return evaluationResult(false, "continue"), nil
				}
			}}
			var out, diag bytes.Buffer
			err = executeWithState(context.Background(), "goal", "request", c, &out, &terminalStatus{writer: &diag}, store)
			snap := store.Snapshot()
			if len(snap.Tasks.Records) != 1 || len(snap.Tasks.Stack) != 0 {
				t.Fatal("runtime invented tasks or failed to finish lineage")
			}
			expected := "completed"
			if scenario == "downstream_rejected" {
				expected = "failed"
			}
			if scenario == "incomplete" {
				expected = "incomplete"
			}
			for _, task := range snap.Tasks.Records {
				if task.Status != expected || task.Objective != "goal" || task.FinishedAt == nil {
					t.Fatalf("task=%+v", task)
				}
			}
			if scenario == "corrected" {
				if err != nil || calls != 2 || len(m.events) != 3 || snap.Intrinsic.GlobalCycle != 1 {
					t.Fatalf("events=%d calls=%d err=%v", len(m.events), calls, err)
				}
			}
			if scenario == "downstream_rejected" {
				if err == nil || len(m.events) != 3 || !strings.Contains(string(m.events[2].Payload), `"status":"error"`) || strings.Contains(string(m.events[2].Payload), `"result"`) {
					t.Fatal("downstream-invalid candidate accepted")
				}
			}
			if scenario == "incomplete" {
				if snap.Intrinsic.GlobalCycle != maxCycles || len(m.events) != 1+maxCycles*2 {
					t.Fatal("global cycle limit changed")
				}
			}
		})
	}
}

type hostStateModule struct {
	process func(state.Event) json.RawMessage
}

func (m *hostStateModule) Process(_ context.Context, _ json.RawMessage, e state.Event) (json.RawMessage, error) {
	return m.process(e), nil
}
func (m *hostStateModule) Close(context.Context) error { return nil }

func TestRunDrainsHostWorkBeforeNextInference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`1`)) }))
	defer server.Close()
	responses := 0
	m := &hostStateModule{process: func(e state.Event) json.RawMessage {
		if e.Source.Kind == "http" {
			responses++
			return json.RawMessage(fmt.Sprintf(`{"status":"mutation","replace":%d}`, responses))
		}
		if e.Source.Kind == "user" || strings.Contains(string(e.Payload), `"operation":"generation"`) {
			raw, _ := json.Marshal(map[string]any{"status": "ignored", "requests": []state.HostRequest{{ID: fmt.Sprintf("r%d", responses), Kind: "http", Payload: json.RawMessage(fmt.Sprintf(`{"method":"GET","url":%q}`, server.URL))}}})
			return raw
		}
		return json.RawMessage(`{"status":"ignored"}`)
	}}
	store, err := state.New(context.Background(), []state.Definition{{Name: "p", Schema: json.RawMessage(`{"type":"integer"}`), Initial: json.RawMessage(`0`), Module: m}}, state.Options{AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	client := testClient{prompt: func(_ context.Context, p string) (openai.TextResult, error) {
		if responses != 1 || !strings.Contains(p, `"value":1`) {
			t.Fatal("initial host work not reflected in generation")
		}
		return openai.TextResult{Text: "answer"}, nil
	}, mutation: func(_ context.Context, i string, _, _ json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		if responses != 2 || !strings.Contains(i, `"value":2`) {
			t.Fatal("generation host work not reflected in evaluation")
		}
		return evaluationResult(true, "done"), nil
	}}
	var out, diag bytes.Buffer
	if err = executeWithState(context.Background(), "goal", "input", client, &out, &terminalStatus{writer: &diag}, store); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot()
	if responses != 2 || snapshot.Intrinsic.GlobalCycle != 1 || snapshot.Intrinsic.HostExecutions != 2 || len(snapshot.Tasks.Stack) != 0 {
		t.Fatal("host work changed agent-cycle accounting")
	}
}

func TestRunPreservesHostBudgetError(t *testing.T) {
	requests := 0
	m := &hostStateModule{process: func(e state.Event) json.RawMessage {
		if e.Source.Kind == "user" {
			return json.RawMessage(`{"status":"ignored"}`)
		}
		requests++
		return json.RawMessage(fmt.Sprintf(`{"status":"ignored","requests":[{"id":"r%d","kind":"http","payload":{"method":"GET","url":"http://example.invalid"}}]}`, requests))
	}}
	store, err := state.New(context.Background(), []state.Definition{{Name: "p", Schema: json.RawMessage(`true`), Initial: json.RawMessage(`0`), Module: m}}, state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	client := testClient{prompt: func(context.Context, string) (openai.TextResult, error) {
		return openai.TextResult{Text: "answer"}, nil
	}, mutation: func(context.Context, string, json.RawMessage, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		t.Fatal("inference continued after host budget exhausted")
		return openai.JSONResult{}, nil
	}}
	var out, diag bytes.Buffer
	err = executeWithState(context.Background(), "goal", "input", client, &out, &terminalStatus{writer: &diag}, store)
	if !errors.Is(err, state.ErrHostBudget) || out.Len() != 0 || store.Snapshot().Intrinsic.HostExecutions != state.MaxHostExecutions {
		t.Fatalf("budget not terminal: %v", err)
	}
}
