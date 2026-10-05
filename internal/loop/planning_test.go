package loop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

func planCall(t *testing.T, id string, cmd planCommand) toolcall.Response {
	t.Helper()
	raw, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	return toolcall.Response{Calls: []toolcall.Call{{ID: id, Name: planningTool, Arguments: raw}}}
}
func TestActorPlansAcceptedEvidenceAndCompletionAcrossCycles(t *testing.T) {
	ctx := context.Background()
	module := &eventModule{fn: func(e runstate.Event) json.RawMessage {
		if e.Source.Kind == "mcp" {
			return json.RawMessage(`{"status":"mutation","replace":{"setting":"enabled"}}`)
		}
		return json.RawMessage(`{"status":"ignored"}`)
	}}
	p, fixture := toolsHarness(t, []runstate.Definition{{Name: "configuration", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{}`), Module: module}}, false)
	evaluations := 0
	p.Evaluator = structuredClient{call: func(_ context.Context, i, _ string, input json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
		evaluations++
		if !strings.Contains(string(input), "accepted_evidence") || !strings.Contains(string(input), "enabled") || spec.Name != "plan_completion" || !strings.Contains(i, "Execution alone is not completion") {
			t.Fatal("evaluator lacks semantic evidence")
		}
		return openai.JSONResult{JSON: json.RawMessage(`{"satisfied":true,"rationale":"The accepted setting satisfies the stated criterion."}`)}, nil
	}}
	turns := 0
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		turns++
		switch turns {
		case 1:
			offered := false
			for _, def := range r.Tools {
				if def.Name == planningTool {
					offered = true
				}
			}
			if !offered {
				t.Fatal("actor cannot produce plans")
			}
			return planCall(t, "plan", planCommand{Action: "revise", Plan: &runstate.Plan{Description: "Inspect configuration", Status: "active", Steps: []runstate.Step{{ID: "inspect", Description: "Obtain authoritative setting", CompletionCriteria: "Accepted setting is available", Status: "active"}, {ID: "monitor", Description: "Observe future change", Status: "pending"}}, CurrentStep: "inspect"}}), nil
		case 2:
			return toolcall.Response{Calls: []toolcall.Call{toolRequest(r, "inspect")}}, nil
		case 3:
			active, _ := p.Store.Active()
			plan := *active.Plan
			if plan.Steps[0].Status != "active" {
				t.Fatal("tool execution auto-completed intent")
			}
			partition := p.Store.Snapshot().Knowledge["configuration"]
			plan.Steps[0].Status = "satisfied"
			plan.Steps[0].Outcome = "Authoritative setting established as enabled"
			plan.Steps[0].Evidence = []runstate.EvidenceReference{{Partition: "configuration", Version: partition.Metadata.Version, Path: "/setting"}}
			plan.Steps[1].Status = "active"
			plan.CurrentStep = "monitor"
			return planCall(t, "claim", planCommand{Action: "revise", Plan: &plan}), nil
		default:
			if !strings.Contains(r.Instructions, `"status":"satisfied"`) || !strings.Contains(r.Instructions, "Authoritative setting established") || strings.Contains(r.Instructions, "fixture result") {
				t.Fatal("projection lost procedural outcome or leaked tool archive")
			}
			return toolcall.Response{Text: "Continue monitoring."}, nil
		}
	})
	if _, err := p.Handle(ctx, &State{Objective: "goal", Prompt: "request"}); err != nil {
		t.Fatal(err)
	}
	if evaluations != 1 || fixture.Calls.Load() != 1 {
		t.Fatal("planning or completion executed incorrect calls")
	}
	if err := p.Store.BeginCycle(ctx); err != nil {
		t.Fatal(err)
	}
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		if len(r.Messages) != 1 || !strings.Contains(r.Instructions, "Authoritative setting established") {
			t.Fatal("continuity depends on conversation replay")
		}
		return toolcall.Response{Text: "Waiting for a change."}, nil
	})
	if _, err := p.Handle(ctx, &State{Objective: "goal", Prompt: "request"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustJSON(t, p.Store.Journal())), "task_plan_evaluated") {
		t.Fatal("completion decision not journaled")
	}
}
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestRejectedCompletionRemainsPartialAndChildLifecycle(t *testing.T) {
	ctx := context.Background()
	p, fixture := toolsHarness(t, []runstate.Definition{{Name: "world", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{"setting":"enabled"}`), Module: &eventModule{}}}, false)
	p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		return openai.JSONResult{JSON: json.RawMessage(`{"satisfied":false,"rationale":"The evidence does not establish the claimed effect."}`)}, nil
	}}
	calls := []toolcall.Call{planCall(t, "create", planCommand{Action: "create_child", Description: "Investigate effect"}).Calls[0]}
	handled, _, err := p.managePlan(ctx, calls)
	if !handled || err != nil {
		t.Fatal(err)
	}
	child, _ := p.Store.Active()
	plan := *child.Plan
	plan.Status = "satisfied"
	plan.Outcome = "Effect established"
	plan.Evidence = []runstate.EvidenceReference{{Partition: "world", Version: 0, Path: "/setting"}}
	_, message, err := p.managePlan(ctx, planCall(t, "claim", planCommand{Action: "revise", Plan: &plan}).Calls)
	if err != nil || !strings.Contains(message, "not confirmed") {
		t.Fatal(message, err)
	}
	child, _ = p.Store.Active()
	if child.Plan.Status != "partial" || len(child.Plan.InformationGaps) != 1 {
		t.Fatal("unsupported completion accepted")
	}
	if _, _, err := p.managePlan(ctx, planCall(t, "complete", planCommand{Action: "complete_child"}).Calls); err == nil {
		t.Fatal("partial child completed")
	}
	if _, _, err := p.managePlan(ctx, planCall(t, "abandon", planCommand{Action: "abandon_child"}).Calls); err != nil {
		t.Fatal(err)
	}
	parent, _ := p.Store.Active()
	if parent.ParentID != "" || fixture.Calls.Load() != 0 {
		t.Fatal("parent not resumed or local planning called MCP")
	}
	if !strings.Contains(string(p.Store.ModelJSON()), "Investigate effect") {
		t.Fatal("child outcome omitted from active parent projection")
	}
	if _, _, err := p.managePlan(ctx, planCall(t, "root-abandon", planCommand{Action: "abandon_child"}).Calls); err == nil {
		t.Fatal("actor terminated root objective without evaluator")
	}
}

func TestCompletionRejectionBoundsUnicodeGap(t *testing.T) {
	ctx := context.Background()
	p, _ := toolsHarness(t, []runstate.Definition{{Name: "world", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{"setting":"enabled"}`), Module: &eventModule{}}}, false)
	p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		return openai.JSONResult{JSON: mustJSON(t, map[string]any{"satisfied": false, "rationale": strings.Repeat("🙂", 140)})}, nil
	}}
	plan := runstate.Plan{Description: "Establish effect", Status: "satisfied", Outcome: "Effect exists", Steps: []runstate.Step{}, Evidence: []runstate.EvidenceReference{{Partition: "world", Version: 0, Path: "/setting"}}}
	if _, _, err := p.managePlan(ctx, planCall(t, "claim", planCommand{Action: "revise", Plan: &plan}).Calls); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	if active.Plan.Status != "partial" || len(active.Plan.InformationGaps[0]) > 512 {
		t.Fatal("unicode decision lost partial progress or exceeded summary budget")
	}
}
