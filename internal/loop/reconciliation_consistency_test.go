package loop

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
)

func consistencyFixture(t *testing.T) *ToolProvider {
	t.Helper()
	p, _ := toolsHarness(t, []runstate.Definition{{Name: "observations", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{"query":{"status":"open"},"items":[],"isError":false}`), Module: &eventModule{}}}, false)
	plan := runstate.Plan{Description: "Inspect items and evaluate remaining work", Status: "active", UnresolvedFocus: "Item inspection is missing; remaining evaluation is incomplete", CurrentStep: "evaluate", Steps: []runstate.Step{
		{ID: "inspect", Description: "Inspect matching items", CompletionCriteria: "Obtain the items matching the open query", Status: "satisfied", Outcome: "Inspection succeeded with zero matching items", Evidence: []runstate.EvidenceReference{{Partition: "observations", Version: 0, Path: "/items"}}},
		{ID: "evaluate", Description: "Evaluate remaining work", Status: "active", InformationGaps: []string{"Evaluation is incomplete"}},
	}}
	if err := p.Store.RevisePlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	return p
}

func continuityResult(valid bool, rationale string) openai.JSONResult {
	return openai.JSONResult{JSON: mustEncode(map[string]any{"continuity_valid": valid, "purpose_preserved": valid, "satisfied": true, "rationale": rationale})}
}

func TestReconciliationRetainedContradictionCorrection(t *testing.T) {
	for _, first := range []string{"no_progress", "identical_progress", "unrelated_progress"} {
		t.Run(first, func(t *testing.T) {
			p := consistencyFixture(t)
			original, _ := p.Store.Active()
			calls, evaluations := 0, 0
			corrected := "Remaining evaluation is incomplete"
			p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, _ json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
				calls++
				if calls == 1 {
					switch first {
					case "identical_progress":
						return withReconciliationFocus(t, reconciliationResult(t, "progress", nil, ""), &original.Plan.UnresolvedFocus), nil
					case "unrelated_progress":
						step := original.Plan.Steps[1]
						step.Outcome = "Initial evaluation observations obtained"
						return reconciliationResult(t, "progress", []progressUpdate{{Step: step}}, ""), nil
					default:
						return reconciliationResult(t, "no_progress", nil, ""), nil
					}
				}
				if !strings.Contains(instructions, "Inspection is already satisfied") || !strings.Contains(instructions, "focus-only correction") {
					t.Fatal("specific consistency correction missing")
				}
				if strings.Count(instructions, "Inspection is already satisfied") != 1 {
					t.Fatal("correction history accumulated")
				}
				active, _ := p.Store.Active()
				if !reflect.DeepEqual(active.Plan, original.Plan) {
					t.Fatal("rejected state committed before correction")
				}
				return withReconciliationFocus(t, reconciliationResult(t, "progress", nil, ""), &corrected), nil
			}}
			p.Evaluator = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
				evaluations++
				if !strings.Contains(instructions, "entire resulting procedural representation") || !strings.Contains(instructions, "no_progress is not an exemption") {
					t.Fatal("retained state consistency not requested")
				}
				var input struct {
					Context reconciliationInput `json:"context"`
					Focus   string              `json:"proposed_unresolved_focus"`
				}
				if err := json.Unmarshal(raw, &input); err != nil {
					t.Fatal(err)
				}
				empty := false
				for _, e := range input.Context.AcceptedEvidence {
					if e.Reference.Path == "" && strings.Contains(string(e.Value), `"items":[]`) || e.Reference.Path == "/items" && string(e.Value) == "[]" {
						empty = true
					}
				}
				if !empty {
					t.Fatal("successful empty observation not passed to evaluator")
				}
				if input.Focus != corrected {
					return continuityResult(false, "Inspection is already satisfied by the successful empty query; remove missing-inspection focus and retain remaining evaluation."), nil
				}
				return continuityResult(true, "Focus agrees with established inspection and remaining work."), nil
			}}
			if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Continue", nil); err != nil {
				t.Fatal(err)
			}
			active, _ := p.Store.Active()
			expected := *original.Plan
			expected.Revision++
			expected.UnresolvedFocus = corrected
			if !reflect.DeepEqual(active.Plan, &expected) || calls != 2 || evaluations != 2 {
				t.Fatal("focus-only correction changed other procedural state or bypassed evaluation")
			}
		})
	}
}

func TestReconciliationInconsistentNoProgressExhaustsCorrectionLimit(t *testing.T) {
	p := consistencyFixture(t)
	before := p.Store.Snapshot()
	calls, evaluations := 0, 0
	p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, _ json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		if calls > 1 && !strings.Contains(instructions, "Missing-inspection focus contradicts established inspection") {
			t.Fatal("rejection feedback missing")
		}
		return reconciliationResult(t, "no_progress", nil, ""), nil
	}}
	p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		evaluations++
		return continuityResult(false, "Missing-inspection focus contradicts established inspection."), nil
	}}
	err := p.reconcile(context.Background(), before, "Continue", nil)
	if !errors.Is(err, errContinuityRejected) || calls != maxReconciliationAttempts || evaluations != maxReconciliationAttempts {
		t.Fatalf("consistency bypassed or incorrect retry limit: %v, calls=%d, evaluations=%d", err, calls, evaluations)
	}
	after := p.Store.Snapshot()
	if !reflect.DeepEqual(before.Tasks, after.Tasks) || !reflect.DeepEqual(before.Knowledge, after.Knowledge) {
		t.Fatal("failed reconciliation changed accepted state")
	}
	if !strings.Contains(string(mustEncode(p.Store.Journal())), "reconciliation_failed") {
		t.Fatal("failure was not traced")
	}
}

func TestReconciliationNoProgressRequiresAvailableEvaluator(t *testing.T) {
	p := reconciliationFixture(t)
	p.Evaluator = nil
	p.Reconciler = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		return reconciliationResult(t, "no_progress", nil, ""), nil
	}}
	if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Continue", nil); err == nil {
		t.Fatal("no_progress bypassed unavailable evaluator")
	}
}

func TestReconciliationEmptyObservationScopeAndCoverage(t *testing.T) {
	for _, mode := range []string{"successful_empty", "failed", "missing", "partial", "action_not_performed"} {
		t.Run(mode, func(t *testing.T) {
			data := `{"query":{"status":"open"},"items":[],"isError":false}`
			focus := "Inspection is complete and no matching items exist"
			if mode == "successful_empty" {
				focus = "Evaluate remaining work"
			}
			if mode == "failed" {
				data = `{"query":{"status":"open"},"items":[],"isError":true}`
			}
			if mode == "missing" {
				data = `{"query":{"status":"open"},"isError":false}`
			}
			if mode == "partial" {
				data = `{"query":{"status":"open"},"items":[],"isError":false,"unprojectable":"` + strings.Repeat("x", maxReconciliationInput*2) + `"}`
				focus = "All query scopes were exhaustively inspected"
			}
			if mode == "action_not_performed" {
				focus = "Execution was completed because the inspection returned no matches"
			}
			p, _ := toolsHarness(t, []runstate.Definition{{Name: "observations", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(data), Module: &eventModule{}}}, false)
			seedProcedure(t, p, "inspect", "Inspect scope")
			p.Reconciler = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
				return withReconciliationFocus(t, reconciliationResult(t, "progress", nil, ""), &focus), nil
			}}
			evaluations := 0
			p.Evaluator = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
				evaluations++
				for _, requirement := range []string{"zero matches within the represented query scope", "Failed or missing results are unknown", "Partial or truncated coverage cannot establish exhaustive absence", "does not establish that an execution action occurred"} {
					if !strings.Contains(instructions, requirement) {
						t.Fatalf("missing evaluator rule: %s", requirement)
					}
				}
				var input struct {
					Context reconciliationInput `json:"context"`
				}
				if err := json.Unmarshal(raw, &input); err != nil {
					t.Fatal(err)
				}
				if mode == "partial" && (!input.Context.EvidenceTruncated || len(input.Context.StateCoverage) == 0 || input.Context.StateCoverage[0].Coverage != "partial") {
					t.Fatal("partial projection lacks explicit coverage")
				}
				if mode == "failed" && !strings.Contains(string(raw), `"isError":true`) {
					t.Fatal("failure metadata lost")
				}
				if mode == "missing" && strings.Contains(string(raw), `"items":[]`) {
					t.Fatal("missing response became empty observation")
				}
				return continuityResult(mode == "successful_empty", "Scoped successful emptiness resolves inspection only; unknown or partial results cannot establish absence or execution."), nil
			}}
			err := p.reconcile(context.Background(), p.Store.Snapshot(), "Continue", nil)
			if mode == "successful_empty" {
				if err != nil || evaluations != 1 {
					t.Fatalf("successful empty inspection rejected: %v", err)
				}
			} else if !errors.Is(err, errContinuityRejected) || evaluations != maxReconciliationAttempts {
				t.Fatalf("unsupported inference accepted: %v evaluations=%d", err, evaluations)
			}
		})
	}
}

func TestReconciliationCorrectionFeedbackBounded(t *testing.T) {
	p := reconciliationFixture(t)
	calls := 0
	p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, _ json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		if calls > 1 {
			const prefix = "The prior proposal failed independent continuity/scope validation: "
			start := strings.Index(instructions, prefix)
			if start < 0 {
				t.Fatal("correction feedback missing")
			}
			feedback := strings.SplitN(instructions[start+len(prefix):], ". Correct the resulting procedural representation", 2)[0]
			if len(feedback) > 512 || strings.Contains(feedback, "�") {
				t.Fatal("feedback exceeded byte bound or broke UTF-8")
			}
		}
		return reconciliationResult(t, "no_progress", nil, ""), nil
	}}
	p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		return continuityResult(false, strings.Repeat("界", 512)), nil
	}}
	if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Continue", nil); !errors.Is(err, errContinuityRejected) || calls != maxReconciliationAttempts {
		t.Fatalf("unexpected correction result: %v calls=%d", err, calls)
	}
}

func TestReconciliationDowngradedStateRequiresConsistency(t *testing.T) {
	p := reconciliationFixture(t)
	ctx := context.Background()
	if _, err := p.Store.Admit(ctx, runstate.Source{Kind: "mcp", ID: "read"}, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	initial := runstate.Plan{Description: "Inspect", Status: "active", UnresolvedFocus: "Inspection is complete", Steps: []runstate.Step{{ID: "inspect", Description: "Inspect", Status: "active"}}}
	if err := p.Store.RevisePlan(ctx, initial); err != nil {
		t.Fatal(err)
	}
	before := p.Store.Snapshot()
	p.Reconciler = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		var input reconciliationInput
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		step := input.Plan.Steps[0]
		step.Status = "satisfied"
		step.Outcome = "Inspection complete"
		step.Evidence = []runstate.EvidenceReference{input.AcceptedEvidence[0].Reference}
		return reconciliationResult(t, "progress", []progressUpdate{{Step: step}}, ""), nil
	}}
	evaluations := 0
	p.Evaluator = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		evaluations++
		var input struct {
			Claims []string `json:"new_satisfaction_claim_ids"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		if len(input.Claims) > 0 {
			return openai.JSONResult{JSON: json.RawMessage(`{"continuity_valid":true,"purpose_preserved":true,"satisfied":false,"rationale":"Inspection evidence is insufficient."}`)}, nil
		}
		return continuityResult(false, "Retained focus claims completion despite the downgraded unresolved inspection."), nil
	}}
	if err := p.reconcile(ctx, before, "Continue", nil); !errors.Is(err, errContinuityRejected) || evaluations != maxReconciliationAttempts {
		t.Fatalf("downgraded representation bypassed consistency: %v evaluations=%d", err, evaluations)
	}
	if after := p.Store.Snapshot(); !reflect.DeepEqual(before.Tasks, after.Tasks) {
		t.Fatal("inconsistent downgraded state committed")
	}
}
