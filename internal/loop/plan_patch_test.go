package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

func patchFixture() *runstate.Plan {
	return &runstate.Plan{Revision: 4, Description: "Assess markets", Status: "active", CurrentStep: "b", Steps: []runstate.Step{
		{ID: "a", Description: "Account inspection", Status: "satisfied", Outcome: "Account inspected", Evidence: []runstate.EvidenceReference{{Partition: "account", Version: 1}}},
		{ID: "b", Description: "Assess BTC", Status: "active", CompletionCriteria: "Assess suitability"},
		{ID: "c", Description: "Assess setup", Status: "pending"},
		{ID: "d", Description: "Review outcomes", Status: "pending"},
	}}
}
func TestPatchPreservesUnmentionedFieldsAndOrderedWork(t *testing.T) {
	old := patchFixture()
	before := mustJSON(t, old)
	next, err := applyPlanPatch(old, &planPatch{Revision: 4, StepUpdates: []json.RawMessage{json.RawMessage(`{"id":"b","description":"Assess ETH"}`), json.RawMessage(`{"id":"c","status":"active"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.Steps[0], old.Steps[0]) || !reflect.DeepEqual(next.Steps[3], old.Steps[3]) || next.Steps[1].CompletionCriteria != old.Steps[1].CompletionCriteria {
		t.Fatal("patch erased unrelated state")
	}
	if string(before) != string(mustJSON(t, old)) {
		t.Fatal("proposal mutated accepted state")
	}
}
func TestPatchAdvancementAndExplicitSelection(t *testing.T) {
	old := patchFixture()
	next, err := applyPlanPatch(old, &planPatch{Revision: 4, StepUpdates: []json.RawMessage{json.RawMessage(`{"id":"b","status":"failed","outcome":"Unsuitable"}`)}})
	if err != nil || next.CurrentStep != "c" {
		t.Fatalf("advancement: %s %v", next.CurrentStep, err)
	}
	next, err = applyPlanPatch(old, &planPatch{Revision: 4, PlanUpdates: json.RawMessage(`{"current_step":"d"}`)})
	if err != nil || next.CurrentStep != "d" {
		t.Fatal("explicit selection lost")
	}
}
func TestInvalidPatchesPreserveAcceptedState(t *testing.T) {
	tests := []planPatch{
		{Revision: 3, RemoveSteps: []string{"b"}},
		{Revision: 4, RemoveSteps: []string{"a"}},
		{Revision: 4, StepUpdates: []json.RawMessage{json.RawMessage(`{"id":"missing","status":"active"}`)}},
		{Revision: 4, RemoveSteps: []string{"b", "b"}},
		{Revision: 4, UnresolvedOrder: []string{"b", "b", "d"}},
		{Revision: 4, UnresolvedOrder: []string{"b"}},
		{Revision: 4, PlanUpdates: json.RawMessage(`{"current_step":"a"}`)},
		{Revision: 4},
	}
	for i, patch := range tests {
		old := patchFixture()
		before := mustJSON(t, old)
		if _, err := applyPlanPatch(old, &patch); err == nil {
			t.Fatalf("invalid patch %d accepted", i)
		}
		if string(before) != string(mustJSON(t, old)) {
			t.Fatal("rejection changed accepted state")
		}
	}
	old := &runstate.Plan{Revision: 1, Steps: []runstate.Step{{ID: "only", Status: "pending"}}}
	if _, err := applyPlanPatch(old, &planPatch{Revision: 1, RemoveSteps: []string{"only"}}); err == nil {
		t.Fatal("removed final step")
	}
}
func TestPatchReordersOnlyUnresolvedSlots(t *testing.T) {
	old := patchFixture()
	next, err := applyPlanPatch(old, &planPatch{Revision: 4, UnresolvedOrder: []string{"d", "c", "b"}})
	if err != nil || !reflect.DeepEqual(next.Steps[0], old.Steps[0]) || next.Steps[1].ID != "d" {
		t.Fatalf("order/history: %v", err)
	}
}
func TestEmptyPlanSchemaAndLegacyOmission(t *testing.T) {
	for _, raw := range []string{`{"action":"revise","plan":{"description":"Goal","status":"active","revision":0}}`, `{"action":"revise","plan":{"description":"Goal","status":"active","revision":0,"steps":[]}}`} {
		if _, err := decodePlanningCommand(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatal("empty plan accepted by advertised schema")
		}
	}
	old := patchFixture()
	next := *old
	next.Steps = next.Steps[:3]
	if err := requireSnapshotSteps(old, &next); err == nil || !strings.Contains(err.Error(), "d") {
		t.Fatal("legacy omission accepted")
	}
}
func TestReconciliationCannotCreateProcedure(t *testing.T) {
	p := reconciliationFixture(t)
	ctx := context.Background()
	plan := runstate.Plan{Description: "Inspect", Status: "active", Steps: []runstate.Step{{ID: "existing", Description: "Inspect", Status: "pending"}}}
	if err := p.Store.RevisePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	before := mustJSON(t, p.Store.Snapshot().Tasks)
	_, err := p.reconciliationPlan(reconciliationInput{TaskID: active.ID}, reconciliationResult(t, "progress", []progressUpdate{{Step: runstate.Step{ID: "invented", Description: "New work", Status: "pending"}}}, "").JSON)
	if err == nil || string(before) != string(mustJSON(t, p.Store.Snapshot().Tasks)) {
		t.Fatal("reconciliation invented procedure")
	}
}

func seedProcedure(t *testing.T, p *ToolProvider, id, description string) {
	t.Helper()
	if err := p.Store.RevisePlan(context.Background(), runstate.Plan{Description: description, Status: "active", Steps: []runstate.Step{{ID: id, Description: description, Status: "pending"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestActorPatchStrategyRevisionAndAtomicRejection(t *testing.T) {
	ctx := context.Background()
	p := reconciliationFixture(t)
	seedProcedure(t, p, "assess", "Assess BTC suitability")
	before := mustJSON(t, p.Store.Snapshot().Tasks)
	patch := &planPatch{Revision: 1, StepUpdates: []json.RawMessage{json.RawMessage(`{"id":"assess","description":"Assess ETH suitability"}`)}}
	call := planCall(t, "change", planCommand{Action: "patch", Patch: patch})
	if _, _, err := p.managePlan(ctx, call.Calls); err == nil || string(before) != string(mustJSON(t, p.Store.Snapshot().Tasks)) {
		t.Fatal("unexplained strategy change accepted")
	}
	evaluations := 0
	p.Evaluator = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
		evaluations++
		if spec.Name != "actor_plan_revision" || !strings.Contains(string(raw), "BTC is unsuitable") {
			t.Fatal("strategy revision bypassed intent evaluator")
		}
		return openai.JSONResult{JSON: json.RawMessage(`{"revision_valid":true,"rationale":"Explicit actor change preserves scope."}`)}, nil
	}}
	call = planCall(t, "change", planCommand{Action: "patch", Patch: patch, RevisionReason: "BTC is unsuitable; assess ETH instead"})
	if _, _, err := p.managePlan(ctx, call.Calls); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	if evaluations != 1 || active.Plan.Revision != 2 || active.Plan.Steps[0].Description != "Assess ETH suitability" {
		t.Fatal("explicit revision not committed")
	}
}

func TestTacticalChoicesRemainWithinCurrentStep(t *testing.T) {
	p := reconciliationFixture(t)
	seedProcedure(t, p, "assess", "Assess BTC suitability")
	p.Evaluator = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
		for _, rule := range []string{"Tactical choices within the current step", "timeframes", "not a new procedure"} {
			if !strings.Contains(instructions, rule) {
				t.Fatalf("missing tactical boundary: %s", rule)
			}
		}
		if spec.Name != "actor_plan_capture" || !strings.Contains(string(raw), "Assess BTC suitability") {
			t.Fatal("accepted procedure missing")
		}
		return openai.JSONResult{JSON: json.RawMessage(`{"capture_required":false,"rationale":"RSI and quotes implement the accepted assessment step."}`)}, nil
	}}
	active, _ := p.Store.Active()
	before := mustJSON(t, p.Store.Snapshot().Tasks)
	required, _, err := p.actorPlanCapture(context.Background(), "Use quotes and RSI on a shorter timeframe to assess BTC suitability.", active.Plan)
	if err != nil || required || string(before) != string(mustJSON(t, p.Store.Snapshot().Tasks)) {
		t.Fatal("tactical choice changed procedure", err)
	}
}

func TestPatchCorrectionBlocksExternalExecutionUntilAcceptance(t *testing.T) {
	p, fixture := toolsHarness(t, nil, false)
	seedProcedure(t, p, "assess", "Assess conditions")
	turns := 0
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		turns++
		active, _ := p.Store.Active()
		switch turns {
		case 1:
			return planCall(t, "bad", planCommand{Action: "patch", Patch: &planPatch{Revision: 1, RemoveSteps: []string{"missing"}}}), nil
		case 2:
			if len(r.Tools) != 1 || r.Tools[0].Name != planningTool || active.Plan.Revision != 1 {
				t.Fatal("rejected patch unlocked execution or changed plan")
			}
			return toolcall.Response{Calls: []toolcall.Call{toolRequest(r, "blocked")}}, nil
		case 3:
			if len(r.Tools) != 1 || active.Plan.Revision != 1 {
				t.Fatal("external attempt escaped correction")
			}
			return planCall(t, "repair", planCommand{Action: "patch", Patch: &planPatch{Revision: 1, StepUpdates: []json.RawMessage{json.RawMessage(`{"id":"assess","status":"partial"}`)}}}), nil
		case 4:
			if len(r.Tools) < 2 || active.Plan.Revision != 2 || active.Plan.CurrentStep != "assess" {
				t.Fatal("accepted repair did not restore procedure")
			}
			return toolcall.Response{Text: "Assessment remains in progress."}, nil
		default:
			t.Fatal("unexpected actor turn")
			return toolcall.Response{}, nil
		}
	})
	if _, err := p.Handle(context.Background(), &State{Objective: "Assess", Prompt: "Assess"}); err != nil {
		t.Fatal(err)
	}
	if turns != 4 || fixture.Calls.Load() != 0 {
		t.Fatal("external operation dispatched during correction")
	}
}

func TestPatchMutationAndPlanSizeBounds(t *testing.T) {
	old := patchFixture()
	additions := []runstate.Step{}
	for i := 0; i < 9; i++ {
		additions = append(additions, runstate.Step{ID: fmt.Sprintf("new%d", i), Description: "Work", Status: "pending"})
	}
	if _, err := applyPlanPatch(old, &planPatch{Revision: 4, AddSteps: additions}); err == nil {
		t.Fatal("oversized patch accepted")
	}
	raw := mustJSON(t, planCommand{Action: "patch", Patch: &planPatch{Revision: 4, AddSteps: additions}})
	if _, err := decodePlanningCommand(context.Background(), raw); err == nil {
		t.Fatal("schema accepted oversized additions")
	}
}

func TestAcceptedCompletionPatchAdvancesWithoutNamingNextStep(t *testing.T) {
	ctx := context.Background()
	p := reconciliationFixture(t)
	initial := runstate.Plan{Description: "Inspect system", Status: "active", Steps: []runstate.Step{
		{ID: "a", Description: "Inspect accepted configuration", Status: "pending"},
		{ID: "b", Description: "Assess configuration", Status: "pending"},
		{ID: "c", Description: "Review assessment", Status: "pending"},
	}}
	if err := p.Store.RevisePlan(ctx, initial); err != nil {
		t.Fatal(err)
	}
	evaluations := 0
	p.Evaluator = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
		evaluations++
		if spec.Name != "plan_completion" || !strings.Contains(string(raw), "accepted_evidence") {
			t.Fatal("completion bypassed evidence evaluator")
		}
		return openai.JSONResult{JSON: json.RawMessage(`{"satisfied":true,"rationale":"Accepted configuration was inspected."}`)}, nil
	}}
	update := json.RawMessage(`{"id":"a","status":"satisfied","outcome":"Configuration inspected","evidence_refs":[{"partition":"configuration","version":0,"path":""}]}`)
	call := planCall(t, "complete", planCommand{Action: "patch", Patch: &planPatch{Revision: 1, StepUpdates: []json.RawMessage{update}}})
	if _, _, err := p.managePlan(ctx, call.Calls); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	if evaluations != 1 || active.Plan.CurrentStep != "b" || len(active.Plan.Steps) != 3 || !reflect.DeepEqual(active.Plan.Steps[2], initial.Steps[2]) {
		t.Fatal("completion failed to advance or lost future work")
	}
}

func TestUnplannedChildRetainsActorCompletionCriteria(t *testing.T) {
	p := reconciliationFixture(t)
	call := planCall(t, "child", planCommand{Action: "create_child", Description: "Assess suitability", CompletionCriteria: "Accepted observations establish suitability"})
	if _, _, err := p.managePlan(context.Background(), call.Calls); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	if active.Plan != nil || !strings.Contains(active.Objective, "Accepted observations establish suitability") || !strings.Contains(string(p.Store.ModelJSON()), "Accepted observations establish suitability") {
		t.Fatal("child initialization erased criteria or accepted an empty plan")
	}
}
