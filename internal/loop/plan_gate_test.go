package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

func TestPlanGateRenewsAcrossProceduralPhases(t *testing.T) {
	for _, phases := range []int{1, 3, 12} {
		t.Run(fmt.Sprint(phases), func(t *testing.T) {
			p, fixture := toolsHarness(t, []runstate.Definition{{Name: "observations", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{}`), Module: &eventModule{fn: func(e runstate.Event) json.RawMessage {
				if e.Source.Kind == "mcp" {
					return json.RawMessage(`{"status":"mutation","replace":{"available":true}}`)
				}
				return json.RawMessage(`{"status":"ignored"}`)
			}}}}, false)
			checks, turns, renewals := 0, 0, 0
			bypassAttempted := false
			p.CheckObjective = func(context.Context, State) (ObjectiveEvaluation, error) {
				checks++
				return ObjectiveEvaluation{Achieved: int(fixture.Calls.Load()) == phases, Rationale: "Verified phase observations."}, nil
			}
			p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
				return continuityResult(true, "Accepted observation establishes the phase inspection."), nil
			}}
			p.Reconciler = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
				var input reconciliationInput
				_ = json.Unmarshal(raw, &input)
				active, _ := p.Store.Active()
				step := active.Plan.Steps[len(active.Plan.Steps)-1]
				step.Status = "satisfied"
				step.Outcome = "The phase observation was retrieved."
				step.Evidence = []runstate.EvidenceReference{{Partition: "observations", Version: p.Store.Snapshot().Knowledge["observations"].Metadata.Version, Path: "/available"}}
				return reconciliationResult(t, "progress", []progressUpdate{{Step: step}}, "", input), nil
			}}
			p.Client = nativeClient(func(_ context.Context, request toolcall.Request) (toolcall.Response, error) {
				turns++
				active, _ := p.Store.Active()
				if !planHasUnresolvedSteps(active.Plan) {
					if len(request.Tools) != 1 || request.Tools[0].Name != planningTool || !strings.Contains(request.Instructions, planRequiredMessage(active.Plan)) {
						t.Fatal("plan boundary did not restrict tools or deliver the directive")
					}
					if fixture.Calls.Load() != int64(renewals) {
						t.Fatal("external work escaped the renewal gate")
					}
					if !bypassAttempted {
						bypassAttempted = true
						if active.Plan == nil {
							return toolcall.Response{Text: "Finish without submitting a plan."}, nil
						}
						return toolcall.Response{Calls: []toolcall.Call{toolRequest(request, "blocked-read")}}, nil
					}
					plan := runstate.Plan{Description: "Inspect observations in phases", Status: "active", Steps: []runstate.Step{}}
					if active.Plan != nil {
						plan = *active.Plan
						plan.Steps = append([]runstate.Step{}, active.Plan.Steps...)
					}
					plan.Steps = append(plan.Steps, runstate.Step{ID: fmt.Sprintf("phase_%d", renewals+1), Description: "Retrieve the next phase observation", Status: "pending"})
					renewals++
					bypassAttempted = false
					return planCall(t, "renew", planCommand{Action: "revise", Plan: &plan}), nil
				}
				if len(request.Tools) < 2 {
					t.Fatal("accepted renewal did not restore ordinary tools")
				}
				return toolcall.Response{Calls: []toolcall.Call{toolRequest(request, "phase-read")}}, nil
			})
			state := &State{Objective: "Retrieve phase observations"}
			if _, err := p.Handle(context.Background(), state); err != nil {
				t.Fatal(err)
			}
			if checks != phases+1 || turns != phases*3 || renewals != phases || fixture.Calls.Load() != int64(phases) || state.ObjectiveEvaluation == nil || !state.ObjectiveEvaluation.Achieved {
				t.Fatalf("incorrect renewal lifecycle: checks=%d turns=%d renewals=%d calls=%d", checks, turns, renewals, fixture.Calls.Load())
			}
			active, _ := p.Store.Active()
			if len(active.Plan.Steps) != phases || planHasUnresolvedSteps(active.Plan) {
				t.Fatal("phase history lost or incomplete")
			}
		})
	}
}

func TestPlanGateRejectsEmptyOrUnsupportedCompletedPlans(t *testing.T) {
	for _, mode := range []string{"empty", "unsupported_completed", "external", "final"} {
		t.Run(mode, func(t *testing.T) {
			p, fixture := toolsHarness(t, nil, false)
			turns := 0
			p.Client = nativeClient(func(_ context.Context, request toolcall.Request) (toolcall.Response, error) {
				turns++
				if len(request.Tools) != 1 || request.Tools[0].Name != planningTool {
					t.Fatal("nonplanning tool advertised")
				}
				if mode == "external" {
					return toolcall.Response{Calls: []toolcall.Call{toolRequest(request, "blocked")}}, nil
				}
				if mode == "final" {
					return toolcall.Response{Text: "Done."}, nil
				}
				plan := runstate.Plan{Description: "Inspect", Status: "active", Steps: []runstate.Step{}}
				if mode == "unsupported_completed" {
					plan.Steps = []runstate.Step{{ID: "inspect", Description: "Inspect", Status: "satisfied"}, {ID: "follow", Description: "Follow up", Status: "pending"}}
				}
				return planCall(t, "bad", planCommand{Action: "revise", Plan: &plan}), nil
			})
			_, err := p.Handle(context.Background(), &State{Objective: "Inspect"})
			active, _ := p.Store.Active()
			if err == nil || turns != maxHostCorrectionResponses || fixture.Calls.Load() != 0 || active.Plan != nil {
				t.Fatalf("gate bypassed: err=%v turns=%d calls=%d", err, turns, fixture.Calls.Load())
			}
		})
	}
}

func TestPlanGateCompletionAndEvaluatorFailure(t *testing.T) {
	for _, mode := range []string{"already_complete", "evaluation_error", "missing_evaluator"} {
		t.Run(mode, func(t *testing.T) {
			p, fixture := toolsHarness(t, nil, false)
			if mode == "missing_evaluator" {
				p.CheckObjective = nil
			} else {
				p.CheckObjective = func(context.Context, State) (ObjectiveEvaluation, error) {
					if mode == "evaluation_error" {
						return ObjectiveEvaluation{}, errors.New("evaluation failed")
					}
					return ObjectiveEvaluation{Achieved: true, Rationale: "Goal already established."}, nil
				}
			}
			p.Client = nativeClient(func(context.Context, toolcall.Request) (toolcall.Response, error) {
				t.Fatal("actor ran at completed or failed boundary")
				return toolcall.Response{}, nil
			})
			before := p.Store.Snapshot()
			state := &State{Objective: "Inspect"}
			_, err := p.Handle(context.Background(), state)
			if (err == nil) != (mode == "already_complete") || fixture.Calls.Load() != 0 || !reflect.DeepEqual(before.Tasks, p.Store.Snapshot().Tasks) {
				t.Fatal("boundary completion or evaluation failure mishandled", err)
			}
		})
	}
}

func TestExhaustedSatisfiedPlanChecksGoalBeforeRenewing(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			p, fixture := toolsHarness(t, []runstate.Definition{{Name: "observation", Schema: json.RawMessage(`{"type":"boolean"}`), Initial: json.RawMessage(`true`), Module: &eventModule{}}}, false)
			ref := runstate.EvidenceReference{Partition: "observation"}
			finished := runstate.Plan{Description: "Current procedure", Status: "satisfied", Outcome: "Initial inspection established", Evidence: []runstate.EvidenceReference{ref}, Steps: []runstate.Step{{ID: "initial", Description: "Initial inspection", Status: "satisfied", Outcome: "Inspection established", Evidence: []runstate.EvidenceReference{ref}}}}
			if err := p.Store.RevisePlan(context.Background(), finished); err != nil {
				t.Fatal(err)
			}
			checks, turns := 0, 0
			p.CheckObjective = func(context.Context, State) (ObjectiveEvaluation, error) {
				checks++
				return ObjectiveEvaluation{Achieved: complete, Rationale: "Checked the root goal against accepted observations."}, nil
			}
			p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
				turns++
				if complete {
					t.Fatal("actor asked to renew after the goal was achieved")
				}
				if turns == 1 {
					if len(r.Tools) != 1 || !strings.Contains(r.Instructions, "You completed your current plan, but the goal is not finished.") {
						t.Fatal("exhausted plan not gated")
					}
					active, _ := p.Store.Active()
					plan := *active.Plan
					plan.Status, plan.Outcome, plan.Evidence = "active", "", nil
					plan.Steps = append(append([]runstate.Step{}, plan.Steps...), runstate.Step{ID: "next", Description: "Next phase inspection", Status: "pending"})
					return planCall(t, "renew", planCommand{Action: "revise", Plan: &plan}), nil
				}
				return toolcall.Response{Text: "Next phase is ready."}, nil
			})
			state := &State{Objective: "Root goal"}
			if _, err := p.Handle(context.Background(), state); err != nil {
				t.Fatal(err)
			}
			if checks != 1 || fixture.Calls.Load() != 0 {
				t.Fatal("goal checked repeatedly or external work escaped")
			}
			active, _ := p.Store.Active()
			if complete {
				if turns != 0 || state.ObjectiveEvaluation == nil {
					t.Fatal("completed goal did not finish immediately")
				}
			} else if turns != 2 || !planHasUnresolvedSteps(active.Plan) || !reflect.DeepEqual(active.Plan.Steps[0], finished.Steps[0]) {
				t.Fatal("renewal lost completed history or did not restore execution")
			}
		})
	}
}

func TestCompletedChildMayResumeParentWithUnfinishedPlan(t *testing.T) {
	p, fixture := toolsExecutionHarness(t, []runstate.Definition{{Name: "observation", Schema: json.RawMessage(`{"type":"boolean"}`), Initial: json.RawMessage(`true`), Module: &eventModule{}}}, false)
	ctx := context.Background()
	parent, _ := p.Store.Active()
	childID, err := p.Store.Push(ctx, "Child procedure", "")
	if err != nil {
		t.Fatal(err)
	}
	ref := runstate.EvidenceReference{Partition: "observation"}
	if err := p.Store.RevisePlan(ctx, runstate.Plan{Description: "Child procedure", Status: "satisfied", Outcome: "Child inspected", Evidence: []runstate.EvidenceReference{ref}, Steps: []runstate.Step{{ID: "inspect", Description: "Inspect child", Status: "satisfied", Outcome: "Child inspected", Evidence: []runstate.EvidenceReference{ref}}}}); err != nil {
		t.Fatal(err)
	}
	turns := 0
	p.Client = nativeClient(func(_ context.Context, request toolcall.Request) (toolcall.Response, error) {
		turns++
		if turns == 1 {
			if len(request.Tools) != 1 {
				t.Fatal("completed child permitted ordinary execution")
			}
			return planCall(t, "complete", planCommand{Action: "complete_child"}), nil
		}
		active, _ := p.Store.Active()
		if active.ID != parent.ID || len(request.Tools) < 2 {
			t.Fatal("parent plan was not rechecked on resumption")
		}
		return toolcall.Response{Text: "Parent work remains."}, nil
	})
	if _, err := p.Handle(ctx, &State{Objective: "Root goal"}); err != nil {
		t.Fatal(err)
	}
	if turns != 2 || fixture.Calls.Load() != 0 || p.Store.Snapshot().Tasks.Records[childID].Status != "completed" {
		t.Fatal("child completion did not resume the existing parent procedure")
	}
}
