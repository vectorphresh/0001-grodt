package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

func TestRepeatedWorkRequestsReplanWithoutCountingCorrections(t *testing.T) {
	p := reconciliationFixture(t)
	seedProcedure(t, p, "inspect", "Inspect configuration")
	actor, proposals := 0, 0
	p.Client = nativeClient(func(_ context.Context, request toolcall.Request) (toolcall.Response, error) {
		actor++
		if actor == 4 {
			active, _ := p.Store.Active()
			if !strings.Contains(request.Instructions, "Replan before repeating") || active.Attempts.Total != 3 || active.Attempts.ConsecutiveNoProgress != 3 || !active.Attempts.ReplanRequired {
				t.Fatal("missing replan feedback or corrections counted as work")
			}
			if active.Plan.Steps[0].Description != "Inspect configuration" || len(p.Store.Snapshot().Knowledge) == 0 {
				t.Fatal("replan discarded existing intent or evidence")
			}
			return toolcall.Response{Text: "The remaining assessment is blocked; preserve acquired evidence."}, nil
		}
		return toolcall.Response{Calls: []toolcall.Call{toolRequest(request, fmt.Sprintf("call-%d", actor))}}, nil
	})
	p.Reconciler = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		proposals++
		var input reconciliationInput
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		return reconciliationResult(t, "no_progress", nil, "", input), nil
	}}
	evaluations := 0
	p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		evaluations++
		return continuityResult(evaluations != 1, "Existing state remains accurate"), nil
	}}
	if _, err := p.Handle(context.Background(), &State{Objective: "Inspect"}); err != nil {
		t.Fatal(err)
	}
	if actor != 4 || proposals != 4 {
		t.Fatalf("actor=%d reconciliation proposals=%d", actor, proposals)
	}
}
