package loop

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

func TestQueryStartsPendingStepEvenWhenReconciliationRejected(t *testing.T) {
	p := reconciliationFixture(t)
	seedProcedure(t, p, "research", "Research trading strategies")
	actor, proposals := 0, 0
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		actor++
		return toolcall.Response{Calls: []toolcall.Call{toolRequest(r, "query")}}, nil
	})
	p.Reconciler = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		proposals++
		var input reconciliationInput
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		if input.Plan.Steps[0].Status != "active" || len(input.Plan.Steps[0].CollectedEvidence) != 1 || input.Plan.Steps[0].CollectedEvidence[0].ResultID != input.AcceptedResults[0].ResultID {
			t.Fatal("query acquisition still depends on semantic reconciliation")
		}
		return reconciliationResult(t, "no_progress", nil, "", input), nil
	}}
	p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		return continuityResult(false, "Correction required"), nil
	}}
	if _, err := p.Handle(context.Background(), &State{Objective: "Research"}); err == nil {
		t.Fatal("rejected reconciliation accepted")
	}
	active, _ := p.Store.Active()
	if actor != 1 || proposals != testReconciliationDeadlineAttempts || active.Plan.Steps[0].Status != "active" || len(active.Plan.Steps[0].CollectedEvidence) != 1 || active.Plan.Steps[0].Outcome != "" || active.Attempts.Total != 1 {
		t.Fatal("corrections lost or duplicated collected evidence, or fabricated an outcome")
	}
}
