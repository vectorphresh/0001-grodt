package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/structured"
)

func TestReconciliationThirtyTwoAttemptsAndPerOperationReset(t *testing.T) {
	for _, boundary := range []string{"host", "continuity"} {
		for _, repair := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/repair=%v", boundary, repair), func(t *testing.T) {
				p := reconciliationFixture(t)
				seedProcedure(t, p, "inspect", "Inspect configuration")
				before := mustEncode(p.Store.Snapshot().Tasks)
				attempts, total := 0, 0
				p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, _ json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
					attempts++
					total++
					if attempts > 1 && !strings.Contains(instructions, "validation") {
						t.Fatal("correction context missing")
					}
					updates := []progressUpdate{}
					action := "no_progress"
					if boundary == "host" && (!repair || attempts < 32) {
						action = "progress"
						updates = append(updates, progressUpdate{Step: runstate.Step{ID: "inspect", Description: "Inspect configuration", CompletionCriteria: "Unaccepted criteria", Status: "active"}})
					}
					result := reconciliationResult(t, action, updates, "")
					var wire map[string]any
					_ = json.Unmarshal(result.JSON, &wire)
					wire["reason"] = "No additional observations change this retained inspection step."
					wire["result_accounting"] = []any{}
					result.JSON = mustEncode(wire)
					return result, nil
				}}
				p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
					return continuityResult(boundary == "host" || (repair && attempts == 32), "Retained inspection representation needs correction."), nil
				}}
				operations := 1
				if repair {
					operations = 2
				}
				for i := 0; i < operations; i++ {
					attempts = 0
					if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Inspect", nil); (err == nil) != repair {
						t.Fatalf("unexpected reconciliation result: %v", err)
					}
					if attempts != 32 {
						t.Fatalf("got %d attempts, want 32", attempts)
					}
				}
				if total != operations*32 || string(before) != string(mustEncode(p.Store.Snapshot().Tasks)) {
					t.Fatal("limit failed to reset or rejected proposals changed state")
				}
			})
		}
	}
}

func TestReconciliationSchemaLimitIsFive(t *testing.T) {
	p := reconciliationFixture(t)
	calls := 0
	p.Reconciler = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		return openai.JSONResult{JSON: json.RawMessage(`{}`)}, nil
	}}
	err := p.reconcile(context.Background(), p.Store.Snapshot(), "Inspect", nil)
	if !errors.Is(err, structured.ErrAttemptsExhausted) || calls != 5 {
		t.Fatalf("schema limit changed: calls=%d err=%v", calls, err)
	}
}
