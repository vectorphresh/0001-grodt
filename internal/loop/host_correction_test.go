package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/mcp"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

func initialCorrectionPlan() runstate.Plan {
	return runstate.Plan{Description: "Inspect account and evaluate opportunities", Status: "active", Steps: []runstate.Step{{ID: "inspect", Description: "Inspect account", Status: "active"}, {ID: "evaluate", Description: "Evaluate opportunities", Status: "pending"}}, CurrentStep: "inspect"}
}
func assertHostCorrection(t *testing.T, r toolcall.Request) {
	t.Helper()
	if len(r.Tools) != 1 || r.Tools[0].Name != planningTool {
		t.Fatal("correction advertised unrelated tools")
	}
	for _, text := range []string{"resubmit grodt_manage_plan alone", "Your attempted planning operation was not executed", "not_dispatched", "not accepted state"} {
		if !strings.Contains(r.Instructions, text) {
			t.Fatalf("missing correction context: %s", text)
		}
	}
}

func TestPlanningCorrectionDistinguishesRepairRequirements(t *testing.T) {
	ctx := context.Background()
	module := &eventModule{fn: func(runstate.Event) json.RawMessage {
		return json.RawMessage(`{"status":"ignored"}`)
	}}
	p, _ := toolsHarness(t, []runstate.Definition{{Name: "inspection", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{"items":[]}`), Module: module}}, false)
	before := p.Store.Snapshot()
	for _, tc := range []struct {
		name   string
		edit   func(*runstate.Plan)
		want   []string
		absent []string
	}{
		{
			name:   "missing_outcome_with_available_evidence",
			edit:   func(p *runstate.Plan) { p.Steps[0].Outcome = "" },
			want:   []string{`Step "inspect" lacks outcome text`, "Write an outcome describing what the referenced evidence established"},
			absent: []string{"lacks evidence_refs", "Leave it unresolved until supported"},
		},
		{
			name:   "missing_reference_with_outcome",
			edit:   func(p *runstate.Plan) { p.Steps[0].Evidence = nil },
			want:   []string{`Step "inspect" lacks evidence_refs`, "Reference accepted state supporting its outcome"},
			absent: []string{"lacks outcome text"},
		},
		{
			name:   "reference_does_not_resolve",
			edit:   func(p *runstate.Plan) { p.Steps[0].Evidence[0].Path = "/value/items" },
			want:   []string{`Step "inspect": correct the evidence reference`, "without /value or /knowledge", "does not establish that the observation is missing"},
			absent: []string{"lacks outcome text", "lacks evidence_refs"},
		},
		{
			name:   "remaining_gap",
			edit:   func(p *runstate.Plan) { p.Steps[0].InformationGaps = []string{"Inspection scope unconfirmed"} },
			want:   []string{`Step "inspect" still has information_gaps`, "Remove a gap only when accepted evidence resolves it"},
			absent: []string{"lacks outcome text", "lacks evidence_refs"},
		},
		{
			name: "completed_current_step",
			edit: func(p *runstate.Plan) { p.CurrentStep = "inspect" },
			want: []string{`current_step points to "inspect", which is satisfied`, "Set current_step to an unresolved step ID, or leave it empty", "Do not downgrade a completed step"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := initialCorrectionPlan()
			plan.Steps[0].Status = "satisfied"
			plan.Steps[0].Outcome = "Inspection found zero matches."
			plan.Steps[0].Evidence = []runstate.EvidenceReference{{Partition: "inspection", Version: 0, Path: "/items"}}
			plan.CurrentStep = "evaluate"
			tc.edit(&plan)
			diagnostics := p.planningCorrectionDiagnostics(ctx, planCall(t, "repair", planCommand{Action: "revise", Plan: &plan}).Calls[0])
			text := strings.Join(diagnostics, "\n")
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Fatalf("missing repair instruction %q: %s", want, text)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(text, absent) {
					t.Fatalf("misleading repair instruction %q: %s", absent, text)
				}
			}
			if len(diagnostics) > 8 {
				t.Fatal("correction diagnostic count exceeded")
			}
			for _, diagnostic := range diagnostics {
				if len(diagnostic) > 256 {
					t.Fatal("correction diagnostic size exceeded")
				}
			}
			if !reflect.DeepEqual(before, p.Store.Snapshot()) {
				t.Fatal("generating correction instructions changed state")
			}
		})
	}
}

func TestPlanningCorrectionWrapperDirectsCompleteRepair(t *testing.T) {
	feedback := (&hostCorrection{ActorIntent: "Inspect and assess the result"}).feedback()
	for _, want := range []string{
		"rejected proposal was not saved; the accepted plan remains unchanged",
		"Fix the listed validation defects and resubmit grodt_manage_plan alone",
		"submit the complete corrected plan, not only the changed step",
		"Preserve all other proposed steps, completion criteria, order, and stated purpose",
		"do not remove them to fix a completion claim",
		"Write outcomes yourself from accepted evidence",
		"does not by itself mean the work must be repeated",
		"Represent any unfinished purpose stated in actor_intent",
		"Do not call other tools in this correction turn",
	} {
		if !strings.Contains(feedback, want) {
			t.Fatalf("missing correction instruction %q", want)
		}
	}
}

func TestPlanningCorrectionExplainsHistoryAndRevisionRepair(t *testing.T) {
	ctx := context.Background()
	module := &eventModule{fn: func(runstate.Event) json.RawMessage {
		return json.RawMessage(`{"status":"ignored"}`)
	}}
	p, _ := toolsHarness(t, []runstate.Definition{{Name: "inspection", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{"items":[]}`), Module: module}}, false)
	accepted := initialCorrectionPlan()
	accepted.Steps[0].Status = "satisfied"
	accepted.Steps[0].CompletionCriteria = "Inspection result available"
	accepted.Steps[0].Outcome = "Inspection found zero matches."
	accepted.Steps[0].Evidence = []runstate.EvidenceReference{{Partition: "inspection", Path: "/items"}}
	accepted.CurrentStep = "evaluate"
	if err := p.Store.RevisePlan(ctx, accepted); err != nil {
		t.Fatal(err)
	}
	before := p.Store.Snapshot()
	for _, tc := range []struct {
		name string
		edit func(*runstate.Plan)
		want string
	}{
		{"omitted_criteria", func(p *runstate.Plan) { p.Steps[0].CompletionCriteria = "" }, `Step "inspect" changes completed completion_criteria`},
		{"changed_reference", func(p *runstate.Plan) { p.Steps[0].Evidence = nil }, `Step "inspect" changes completed evidence_refs`},
		{"removed_completed_step", func(p *runstate.Plan) { p.Steps = p.Steps[1:] }, `Restore completed step "inspect"`},
		{"incremented_revision", func(p *runstate.Plan) { p.Revision++ }, "Set plan.revision to 1, the currently accepted revision; you submitted 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			active, _ := p.Store.Active()
			plan := *active.Plan
			plan.Steps = append([]runstate.Step(nil), active.Plan.Steps...)
			plan.Steps = append(plan.Steps, runstate.Step{ID: "follow_up", Description: "Actor-declared follow-up", Status: "pending"})
			tc.edit(&plan)
			call := planCall(t, "repair", planCommand{Action: "revise", Plan: &plan}).Calls[0]
			feedback := p.newHostCorrection(ctx, call).feedback()
			// Decode the correction data so quoted IDs are not JSON-escaped.
			var data hostCorrection
			if err := json.Unmarshal([]byte(feedback[strings.LastIndex(feedback, "\n")+1:]), &data); err != nil {
				t.Fatal(err)
			}
			instructions := strings.Join(data.Diagnostics, "\n")
			if !strings.Contains(instructions, tc.want) {
				t.Fatalf("missing specific repair: %s", instructions)
			}
			if tc.name == "incremented_revision" {
				if !strings.Contains(instructions, "Rejected proposals do not increment the revision") {
					t.Fatal("revision repair lacks rejected-proposal semantics")
				}
			} else if !strings.Contains(instructions, "including completion_criteria, outcome, and evidence_refs") || !strings.Contains(instructions, "Keep new unfinished steps") && !strings.Contains(instructions, "keep new unfinished steps") {
				t.Fatal("history repair does not explain preservation")
			}
			for _, diagnostic := range data.Diagnostics {
				if len(diagnostic) > 256 {
					t.Fatal("diagnostic exceeded bound")
				}
			}
			if !reflect.DeepEqual(before, p.Store.Snapshot()) {
				t.Fatal("correction mutated accepted state")
			}
		})
	}
}

func TestExclusiveHostCorrectionMixedReadAndSideEffect(t *testing.T) {
	for _, effect := range []bool{false, true} {
		t.Run(fmt.Sprint(effect), func(t *testing.T) {
			p, fixture := toolsHarness(t, nil, false)
			original := initialCorrectionPlan()
			original.Steps[0].Status = "satisfied"
			before := p.Store.Snapshot()
			turns := 0
			p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
				turns++
				switch turns {
				case 1:
					response := planCall(t, "mixed-plan", planCommand{Action: "revise", Plan: &original})
					companion := toolRequest(r, "companion")
					if effect {
						companion.Arguments = json.RawMessage(`{"key":"submit_order"}`)
					}
					response.Calls = append(response.Calls, companion)
					return response, nil
				case 2:
					assertHostCorrection(t, r)
					if !strings.Contains(r.Instructions, "lacks outcome text") || !strings.Contains(r.Instructions, "lacks evidence_refs") || !strings.Contains(r.Instructions, "Set current_step to an unresolved step ID") || !strings.Contains(r.Instructions, "do not remove them to fix a completion claim") {
						t.Fatal("content and invocation feedback not returned together")
					}
					now := p.Store.Snapshot()
					if !reflect.DeepEqual(before.Tasks, now.Tasks) || !reflect.DeepEqual(before.Knowledge, now.Knowledge) || fixture.Calls.Load() != 0 {
						t.Fatal("rejected mixed batch changed accepted state")
					}
					corrected := initialCorrectionPlan()
					return planCall(t, "corrected", planCommand{Action: "revise", Plan: &corrected}), nil
				case 3:
					active, _ := p.Store.Active()
					if active.Plan == nil || active.Plan.Steps[0].Status != "active" || len(r.Tools) < 2 || strings.Contains(r.Instructions, "attempted_arguments") {
						t.Fatal("accepted correction did not restore normal generation")
					}
					if fixture.Calls.Load() != 0 {
						t.Fatal("companion automatically replayed")
					}
					if effect {
						return toolcall.Response{Text: "Plan saved; no external effect requested."}, nil
					}
					return toolcall.Response{Calls: []toolcall.Call{toolRequest(r, "actor-read")}}, nil
				default:
					return toolcall.Response{Text: "Read completed."}, nil
				}
			})
			if _, err := p.Handle(context.Background(), &State{Objective: "Inspect"}); err != nil {
				t.Fatal(err)
			}
			expected := int64(1)
			if effect {
				expected = 0
			}
			if fixture.Calls.Load() != expected {
				t.Fatal("external execution not exclusively actor-authorized")
			}
			journal := string(mustEncode(p.Store.Journal()))
			if !strings.Contains(journal, "plan_initialization_required") || !strings.Contains(journal, "exclusive_host_correction_accepted") {
				t.Fatal("correction audit missing")
			}
		})
	}
}

func TestExclusiveHostCorrectionBypassAndExhaustion(t *testing.T) {
	for _, mode := range []string{"external_only", "mixed_again", "final_text", "invalid_content"} {
		t.Run(mode, func(t *testing.T) {
			p, fixture := toolsHarness(t, nil, false)
			initial := initialCorrectionPlan()
			if err := p.Store.RevisePlan(context.Background(), initial); err != nil {
				t.Fatal(err)
			}
			before := p.Store.Snapshot()
			turns := 0
			p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
				turns++
				plan := initialCorrectionPlan()
				plan.Revision = 1
				if turns == 1 {
					response := planCall(t, "initial", planCommand{Action: "revise", Plan: &plan})
					response.Calls = append(response.Calls, toolRequest(r, "companion"))
					return response, nil
				}
				assertHostCorrection(t, r)
				switch mode {
				case "external_only":
					return toolcall.Response{Calls: []toolcall.Call{{ID: "bypass", Name: "grodt_tool_001", Arguments: json.RawMessage(`{}`)}}}, nil
				case "final_text":
					return toolcall.Response{Text: "I will skip planning and finish."}, nil
				case "invalid_content":
					plan.Steps[0].Status = "satisfied"
					return planCall(t, "invalid", planCommand{Action: "revise", Plan: &plan}), nil
				default:
					response := planCall(t, "mixed", planCommand{Action: "revise", Plan: &plan})
					response.Calls = append(response.Calls, toolcall.Call{ID: "external", Name: "grodt_tool_001", Arguments: json.RawMessage(`{}`)})
					return response, nil
				}
			})
			_, err := p.Handle(context.Background(), &State{Objective: "Inspect"})
			if err == nil || !strings.Contains(err.Error(), "exclusive host correction exhausted") || turns != maxHostCorrectionResponses+1 || fixture.Calls.Load() != 0 {
				t.Fatalf("correction bypassed or unbounded: %v turns=%d calls=%d", err, turns, fixture.Calls.Load())
			}
			after := p.Store.Snapshot()
			if !reflect.DeepEqual(before.Tasks, after.Tasks) || !reflect.DeepEqual(before.Knowledge, after.Knowledge) {
				t.Fatal("failed correction changed accepted state")
			}
			if !strings.Contains(string(mustEncode(p.Store.Journal())), "exclusive_host_correction_exhausted") {
				t.Fatal("exhaustion not audited")
			}
		})
	}
}

func TestExclusiveHostCorrectionUsableResponseOpportunities(t *testing.T) {
	p, fixture := toolsExecutionHarness(t, nil, false)
	turns := 0
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		turns++
		plan := initialCorrectionPlan()
		plan.Revision = 1
		if turns == 1 {
			response := planCall(t, "initial", planCommand{Action: "revise", Plan: &plan})
			response.Calls = append(response.Calls, toolRequest(r, "companion"))
			return response, nil
		}
		if turns <= maxHostCorrectionResponses+3 {
			assertHostCorrection(t, r)
		}
		switch turns {
		case 2:
			return toolcall.Response{}, recoveryGenerationFailure{}
		case 3:
			return toolcall.Response{}, nil
		case maxHostCorrectionResponses + 3:
			return planCall(t, "corrected", planCommand{Action: "revise", Plan: &plan}), nil
		case maxHostCorrectionResponses + 4:
			return toolcall.Response{Text: "Plan saved."}, nil
		default:
			plan.Steps[0].Status = "satisfied"
			return planCall(t, "invalid", planCommand{Action: "revise", Plan: &plan}), nil
		}
	})
	if _, err := p.Handle(context.Background(), &State{Objective: "Inspect"}); err != nil {
		t.Fatal(err)
	}
	if turns != maxHostCorrectionResponses+4 || fixture.Calls.Load() != 0 {
		t.Fatal("unusable responses consumed opportunities or companion executed")
	}
	if !strings.Contains(string(mustEncode(p.Store.Journal())), `"code":"exclusive_host_correction_accepted"`) || !strings.Contains(string(mustEncode(p.Store.Journal())), `"attempt":10`) {
		t.Fatal("tenth accepted correction not audited")
	}
}

func TestExclusiveHostCorrectionOversizedAndAmbiguousBoundaries(t *testing.T) {
	for _, mode := range []string{"oversized", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			p, fixture := toolsHarness(t, nil, false)
			turns := 0
			p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
				turns++
				plan := initialCorrectionPlan()
				if turns == 1 {
					response := planCall(t, "initial", planCommand{Action: "revise", Plan: &plan})
					if mode == "ambiguous" {
						response.Calls = append(response.Calls, planCall(t, "second", planCommand{Action: "revise", Plan: &plan}).Calls[0])
					} else {
						response.Calls[0].Arguments = mustEncode(map[string]string{"oversized_marker": strings.Repeat("x", mcp.MaxArgumentBytes+1)})
						response.Calls = append(response.Calls, toolRequest(r, "companion"))
					}
					return response, nil
				}
				if mode == "ambiguous" {
					t.Fatal("ambiguous planning operations allowed another turn")
				}
				if turns == 2 {
					assertHostCorrection(t, r)
					if !strings.Contains(r.Instructions, `"reconstruct_arguments":true`) || strings.Contains(r.Instructions, "oversized_marker") || strings.Contains(fmt.Sprint(r.Messages), "oversized_marker") {
						t.Fatal("oversized request retained or reconstructed by host")
					}
					if !strings.Contains(r.Instructions, fmt.Sprint(mcp.MaxArgumentBytes)) {
						t.Fatal("size limit feedback missing")
					}
					return planCall(t, "reconstructed", planCommand{Action: "revise", Plan: &plan}), nil
				}
				return toolcall.Response{Text: "Plan reconstructed."}, nil
			})
			_, err := p.Handle(context.Background(), &State{Objective: "Inspect"})
			if mode == "ambiguous" {
				if err == nil || !strings.Contains(err.Error(), "ambiguous exclusive host operations") || turns != 1 {
					t.Fatalf("ambiguous operations accepted: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if fixture.Calls.Load() != 0 {
				t.Fatal("rejected companion executed")
			}
		})
	}
}

func TestExclusiveHostCorrectionCancelled(t *testing.T) {
	p, fixture := toolsHarness(t, nil, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	turns := 0
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		turns++
		if turns == 2 {
			cancel()
			return toolcall.Response{}, ctx.Err()
		}
		plan := initialCorrectionPlan()
		response := planCall(t, "initial", planCommand{Action: "revise", Plan: &plan})
		response.Calls = append(response.Calls, toolRequest(r, "companion"))
		return response, nil
	})
	if _, err := p.Handle(ctx, &State{Objective: "Inspect"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not terminal: %v", err)
	}
	if fixture.Calls.Load() != 0 {
		t.Fatal("cancelled correction dispatched companion")
	}
}

func TestExclusiveHostCorrectionKeepsExternalRecoverySeparate(t *testing.T) {
	p, fixture := toolsExecutionHarness(t, nil, false)
	fixture.Call = func(*http.Request, string, json.RawMessage) (json.RawMessage, error) {
		return nil, errors.New("transport failure")
	}
	turns := 0
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		turns++
		plan := initialCorrectionPlan()
		plan.Revision = 1
		switch turns {
		case 1:
			return toolcall.Response{Calls: []toolcall.Call{toolRequest(r, "uncertain")}}, nil
		case 2:
			response := planCall(t, "mixed", planCommand{Action: "revise", Plan: &plan})
			response.Calls = append(response.Calls, toolRequest(r, "companion"))
			return response, nil
		case 3:
			assertHostCorrection(t, r)
			if !strings.Contains(r.Instructions, "External recovery remains pending") {
				t.Fatal("external certainty or precedence lost")
			}
			plan.Steps[0].Status = "satisfied"
			return planCall(t, "invalid", planCommand{Action: "revise", Plan: &plan}), nil
		case 4:
			assertHostCorrection(t, r)
			return planCall(t, "corrected", planCommand{Action: "revise", Plan: &plan}), nil
		default:
			if turns != 5 || !strings.Contains(r.Instructions, "outcome_unknown") || len(r.Tools) < 2 {
				t.Fatal("host opportunities consumed or erased external recovery")
			}
			return toolcall.Response{Text: "Plan accepted; external outcome remains unknown."}, nil
		}
	})
	if _, err := p.Handle(context.Background(), &State{Objective: "Inspect"}); err != nil {
		t.Fatal(err)
	}
	if fixture.Calls.Load() != 1 {
		t.Fatal("uncertain external operation or companion replayed")
	}
}
