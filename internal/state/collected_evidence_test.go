package state

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestCollectedQueriesStartStepAndAccumulate(t *testing.T) {
	ctx := context.Background()
	module := &processor{fn: func(_ json.RawMessage, e Event) (json.RawMessage, error) {
		if e.Source.Kind != "mcp" {
			return json.RawMessage(`{"status":"ignored"}`), nil
		}
		var payload struct {
			Tool   string          `json:"tool"`
			Result json.RawMessage `json:"result"`
		}
		_ = json.Unmarshal(e.Payload, &payload)
		return encode(moduleResult{Status: "mutation", Replace: encode(map[string]any{"sources": map[string]any{e.Source.ID: map[string]any{payload.Tool: map[string]any{"result": payload.Result, "correlation": e.Correlation}}}})}), nil
	}}
	s := newStore(t, Definition{Name: "mcp_tools", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{"sources":{}}`), Module: module})
	if _, err := s.Push(ctx, "Research", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RevisePlan(ctx, Plan{Description: "Research", Status: "active", Steps: []Step{{ID: "research", Description: "Research", Status: "pending"}, {ID: "execute", Description: "Execute", Status: "pending"}}}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		ids, err := s.QueueAgentWork(ctx, []AgentWork{{OperationID: "op", Turn: uint64(i), CallID: fmt.Sprint(i), Server: "research", Tool: "query", Arguments: encode(map[string]int{"query": i})}})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ActivateAgentWork(ctx, ids[0]); err != nil {
			t.Fatal(err)
		}
		result := encode(map[string]any{"content": []any{}, "structuredContent": map[string]int{"value": i}, "isError": false})
		if err := s.RecordAgentOutcome(ctx, result, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AdmitAgentOutcome(ctx, result); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishAgentWork(ctx); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordCollectedEvidence(ctx, ids[0]); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordCollectedEvidence(ctx, ids[0]); err != nil {
			t.Fatal(err)
		}
		active, _ := s.Active()
		step := active.Plan.Steps[0]
		if step.Status != "active" || len(step.CollectedEvidence) != i || step.CollectedEvidence[i-1].ResultID != ids[0] || step.CollectedEvidence[i-1].Reference == nil || step.CollectedEvidence[i-1].Reference.Version != uint64(i) || active.Plan.Steps[1].Status != "pending" {
			t.Fatal("acquisition not attributed, duplicated, or marked complete")
		}
		if got := s.Snapshot().Tasks.Records[step.CollectedEvidence[0].ResultID].Result; !bytes.Contains([]byte(got), []byte(`"value":1`)) {
			t.Fatal("refresh lost original query data")
		}
	}
	active, _ := s.Active()
	plan := *active.Plan
	plan.Steps[0].CollectedEvidence = nil // actor schemas omit host-owned history
	plan.Steps[0].Outcome = "Research remains underway"
	if err := s.RevisePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	active, _ = s.Active()
	if len(active.Plan.Steps[0].CollectedEvidence) != 2 {
		t.Fatal("actor revision erased acquisition history")
	}
	plan = *active.Plan
	plan.Steps[0].Status = "pending"
	if err := s.RevisePlan(ctx, plan); err == nil {
		t.Fatal("step with collected data regressed to pending")
	}
}

func TestFailedQueryDoesNotStartPendingStep(t *testing.T) {
	for _, code := range []string{"mcp_operation_failed", "tool_execution_error"} {
		t.Run(code, func(t *testing.T) {
			ctx := context.Background()
			s := planStore(t)
			if err := s.RevisePlan(ctx, Plan{Description: "Research", Status: "active", Steps: []Step{{ID: "research", Description: "Research", Status: "pending"}}}); err != nil {
				t.Fatal(err)
			}
			ids, err := s.QueueAgentWork(ctx, []AgentWork{{OperationID: "op", Turn: 1, CallID: "failed", Server: "research", Tool: "query", Arguments: json.RawMessage(`{}`)}})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.ActivateAgentWork(ctx, ids[0]); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordAgentOutcome(ctx, json.RawMessage(`{"isError":true}`), code); err != nil {
				t.Fatal(err)
			}
			if err := s.FinishAgentWork(ctx); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordCollectedEvidence(ctx, ids[0]); err != nil {
				t.Fatal(err)
			}
			active, _ := s.Active()
			if active.Plan.Steps[0].Status != "pending" || len(active.Plan.Steps[0].CollectedEvidence) != 0 {
				t.Fatal("failed result became collected evidence")
			}
			candidate := *active.Plan
			candidate.Steps[0].CollectedEvidence = []CollectedEvidence{{ResultID: ids[0]}}
			before := s.JSON()
			if err := s.RevisePlan(ctx, candidate); err == nil || !bytes.Equal(before, s.JSON()) {
				t.Fatal("actor fabricated acquisition history")
			}
		})
	}
}
