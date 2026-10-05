package loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

func reconciliationFixture(t *testing.T) *ToolProvider {
	t.Helper()
	module := &eventModule{fn: func(e runstate.Event) json.RawMessage {
		if e.Source.Kind == "mcp" {
			return json.RawMessage(`{"status":"mutation","replace":{"setting":"enabled"}}`)
		}
		return json.RawMessage(`{"status":"ignored"}`)
	}}
	p, _ := toolsHarness(t, []runstate.Definition{{Name: "configuration", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{}`), Module: module}, {Name: "unrelated", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{"private_context":"unrelated_partition_sentinel"}`), Module: &eventModule{}}}, false)
	return p
}
func reconciliationResult(t *testing.T, action string, updates []progressUpdate, focus string) openai.JSONResult {
	wire := make([]map[string]any, 0, len(updates))
	for _, update := range updates {
		var step map[string]any
		if err := json.Unmarshal(mustJSON(t, update.Step), &step); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"completion_criteria", "outcome"} {
			if _, ok := step[key]; !ok {
				step[key] = ""
			}
		}
		for _, key := range []string{"evidence_refs", "information_gaps"} {
			if _, ok := step[key]; !ok {
				step[key] = []any{}
			}
		}
		wire = append(wire, map[string]any{"step": step, "intent_quote": update.IntentQuote})
	}
	return openai.JSONResult{JSON: mustJSON(t, map[string]any{"action": action, "updates": wire, "current_step": focus})}
}
func TestAutomaticReconciliationBeforeNextActor(t *testing.T) {
	ctx := context.Background()
	p := reconciliationFixture(t)
	actorCalls, reconciliations, evaluations := 0, 0, 0
	p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
		reconciliations++
		if spec.Name != "progress_reconciliation" || len(raw) > maxReconciliationInput || strings.Contains(string(raw), "unrelated_partition_sentinel") || strings.Contains(string(raw), "inputSchema") || strings.Contains(instructions, "Current actionable GRODT state") {
			t.Fatal("reconciliation received unrelated state or catalog")
		}
		var input reconciliationInput
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		if len(input.AcceptedEvidence) == 0 {
			t.Fatal("accepted evidence missing")
		}
		evidence := []runstate.EvidenceReference{input.AcceptedEvidence[0].Reference}
		return reconciliationResult(t, "progress", []progressUpdate{{Step: runstate.Step{ID: "initial", Description: "Establish initial configuration", CompletionCriteria: "Obtain accepted setting", Status: "satisfied", Outcome: "Initial setting established as enabled", Evidence: evidence}, IntentQuote: "Establish initial configuration"}, {Step: runstate.Step{ID: "investigation", Description: "Evaluate remaining uncertainty", Status: "active", InformationGaps: []string{"Remaining uncertainty unresolved"}}, IntentQuote: "Evaluate remaining uncertainty"}}, "investigation"), nil
	}}
	p.Evaluator = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
		evaluations++
		if spec.Name != "plan_completion" || !strings.Contains(string(raw), "enabled") {
			t.Fatal("completion path bypassed")
		}
		return openai.JSONResult{JSON: json.RawMessage(`{"satisfied":true,"rationale":"Accepted setting supports the stated intent."}`)}, nil
	}}
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		actorCalls++
		if actorCalls == 1 {
			return toolcall.Response{Text: "Establish initial configuration. Evaluate remaining uncertainty.", Calls: []toolcall.Call{toolRequest(r, "read")}}, nil
		}
		if !strings.Contains(r.Instructions, "Initial setting established as enabled") || !strings.Contains(r.Instructions, `"current_step":"investigation"`) {
			t.Fatal("next actor lacks automatically reconciled progress")
		}
		return toolcall.Response{Text: "Waiting for further information."}, nil
	})
	if _, err := p.Handle(ctx, &State{Objective: "Inspect system", Prompt: "request"}); err != nil {
		t.Fatal(err)
	}
	if actorCalls != 2 || reconciliations != 1 || evaluations != 1 {
		t.Fatal("reconciliation recursively triggered or not run before actor")
	}
	if len(p.Store.Snapshot().Tasks.Records) != 2 {
		t.Fatal("reconciliation created native invocation tasks")
	}
	if !strings.Contains(string(mustJSON(t, p.Store.Journal())), "procedural_reconciliation") {
		t.Fatal("reconciliation audit missing")
	}
}
func TestReconciliationPartialExistingPlanAndRejectedSatisfaction(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial", true: "rejected"}[reject], func(t *testing.T) {
			ctx := context.Background()
			p := reconciliationFixture(t)
			initial := runstate.Plan{Description: "Inspect system", Status: "active", Steps: []runstate.Step{{ID: "property", Description: "Characterize a property", CompletionCriteria: "Evidence adequately characterizes the property", Status: "active"}}, CurrentStep: "property"}
			if err := p.Store.RevisePlan(ctx, initial); err != nil {
				t.Fatal(err)
			}
			before := p.Store.Snapshot()
			if _, err := p.Store.Admit(ctx, runstate.Source{Kind: "mcp", ID: "read"}, json.RawMessage(`{}`)); err != nil {
				t.Fatal(err)
			}
			evaluations := 0
			p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
				evaluations++
				return openai.JSONResult{JSON: json.RawMessage(`{"satisfied":false,"rationale":"The property remains uncharacterized."}`)}, nil
			}}
			p.Reconciler = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
				var input reconciliationInput
				_ = json.Unmarshal(raw, &input)
				step := input.Plan.Steps[0]
				step.Status = "partial"
				step.Outcome = "Accepted setting obtained"
				step.InformationGaps = []string{"The property remains uncharacterized."}
				step.Evidence = []runstate.EvidenceReference{input.AcceptedEvidence[0].Reference}
				if reject {
					step.Status = "satisfied"
					step.InformationGaps = nil
				}
				return reconciliationResult(t, "progress", []progressUpdate{{Step: step}}, ""), nil
			}}
			world := string(p.Store.Snapshot().Knowledge["configuration"].Value)
			if err := p.reconcile(ctx, before, "Characterize the property", nil); err != nil {
				t.Fatal(err)
			}
			task, _ := p.Store.Active()
			if len(task.Plan.Steps) != 1 || task.Plan.Steps[0].Status != "partial" || len(task.Plan.Steps[0].InformationGaps) != 1 {
				t.Fatal("existing plan lost or satisfaction fabricated")
			}
			if evaluations != map[bool]int{false: 0, true: 1}[reject] || world != string(p.Store.Snapshot().Knowledge["configuration"].Value) {
				t.Fatal("incorrect evaluation or domain state changed")
			}
		})
	}
}
func TestNoProgressBoundedAndFailedReconciliationAtomic(t *testing.T) {
	ctx := context.Background()
	p := reconciliationFixture(t)
	p.Reconciler = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		return reconciliationResult(t, "no_progress", []progressUpdate{}, ""), nil
	}}
	before := p.Store.Snapshot()
	projection := len(p.Store.ModelJSON())
	for i := 0; i < 40; i++ {
		if err := p.reconcile(ctx, before, "An inconclusive observation", nil); err != nil {
			t.Fatal(err)
		}
	}
	if active, _ := p.Store.Active(); active.Plan != nil || len(p.Store.ModelJSON()) > projection+16 {
		t.Fatal("no-progress cycles created synthetic history")
	}
	for _, mode := range []string{"malformed", "invalid", "transport", "oversized", "invented_evidence", "invented_intent"} {
		t.Run(mode, func(t *testing.T) {
			p.Reconciler = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
				switch mode {
				case "malformed":
					return openai.JSONResult{JSON: json.RawMessage(`{`)}, nil
				case "invalid":
					return openai.JSONResult{JSON: json.RawMessage(`{"action":"invent","updates":[],"current_step":""}`)}, nil
				case "transport":
					return openai.JSONResult{}, errors.New("provider failed")
				case "oversized":
					return openai.JSONResult{JSON: mustJSON(t, map[string]any{"unrestricted": strings.Repeat("x", maxReconciliationOutput+1)})}, nil
				}
				step := runstate.Step{ID: "new", Description: "New intent", Status: "partial", Evidence: []runstate.EvidenceReference{{Partition: "configuration", Version: 99, Path: "/fabricated"}}}
				quote := "inconclusive"
				if mode == "invented_intent" {
					quote = "Unexpressed strategy"
					step.Evidence = nil
				}
				return reconciliationResult(t, "progress", []progressUpdate{{Step: step, IntentQuote: quote}}, ""), nil
			}}
			prior := p.Store.Snapshot()
			if err := p.reconcile(ctx, prior, "An inconclusive observation", nil); err == nil {
				t.Fatal("invalid reconciliation accepted")
			}
			after := p.Store.Snapshot()
			if string(mustJSON(t, prior.Tasks)) != string(mustJSON(t, after.Tasks)) || string(mustJSON(t, prior.Knowledge)) != string(mustJSON(t, after.Knowledge)) {
				t.Fatal("failed reconciliation corrupted state")
			}
		})
	}
}
func TestReconciliationInputFreshnessAndBounds(t *testing.T) {
	ctx := context.Background()
	p := reconciliationFixture(t)
	before := p.Store.Snapshot()
	if _, err := p.Store.Admit(ctx, runstate.Source{Kind: "mcp", ID: "first"}, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	version := p.Store.Snapshot().Knowledge["configuration"].Metadata.Version
	plan := runstate.Plan{Description: "Inspect system", Status: "active", Steps: []runstate.Step{{ID: "initial", Description: "Establish initial configuration", Status: "satisfied", Outcome: "Initial configuration established", Evidence: []runstate.EvidenceReference{{Partition: "configuration", Version: version, Path: "/setting"}}}}}
	if err := p.Store.RevisePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	before = p.Store.Snapshot()
	if _, err := p.Store.Admit(ctx, runstate.Source{Kind: "mcp", ID: "refresh"}, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	input := p.reconciliationInput(before, strings.Repeat("🙂", 5000), nil)
	if len(mustJSON(t, input)) > maxReconciliationInput || len(input.ActorIntent) > 4096 || len(input.AcceptedEvidence) == 0 {
		t.Fatal("fresh references or input bounds missing")
	}
	if input.Plan.Steps[0].Status != "satisfied" || input.AcceptedEvidence[0].Reference.Version == version {
		t.Fatal("refresh invalidated historical outcome or used superseded evidence")
	}
}

func TestReconciliationAllowsRepeatedRetrievalForNewIntent(t *testing.T) {
	ctx := context.Background()
	p := reconciliationFixture(t)
	turns, reconciliations := 0, 0
	p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		return openai.JSONResult{JSON: json.RawMessage(`{"satisfied":true,"rationale":"The accepted setting supports this observation intent."}`)}, nil
	}}
	p.Reconciler = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		reconciliations++
		var input reconciliationInput
		_ = json.Unmarshal(raw, &input)
		id, description := "initial", "Establish initial configuration"
		if reconciliations == 2 {
			id, description = "current", "Determine current configuration"
			if input.Plan.Steps[0].Status != "satisfied" || input.EvidenceState[0].Freshness != "superseded" {
				t.Fatal("refresh erased fixed initial observation")
			}
		}
		step := runstate.Step{ID: id, Description: description, Status: "satisfied", Outcome: "Setting observed as enabled", Evidence: []runstate.EvidenceReference{input.AcceptedEvidence[0].Reference}}
		return reconciliationResult(t, "progress", []progressUpdate{{Step: step, IntentQuote: description}}, ""), nil
	}}
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		turns++
		switch turns {
		case 1:
			return toolcall.Response{Text: "Establish initial configuration", Calls: []toolcall.Call{toolRequest(r, "initial")}}, nil
		case 2:
			return toolcall.Response{Text: "Determine current configuration", Calls: []toolcall.Call{toolRequest(r, "current")}}, nil
		default:
			return toolcall.Response{Text: "Observations complete."}, nil
		}
	})
	if _, err := p.Handle(ctx, &State{Objective: "Inspect", Prompt: "request"}); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	if len(active.Plan.Steps) != 2 || active.Plan.Steps[0].Evidence[0].Version >= active.Plan.Steps[1].Evidence[0].Version {
		t.Fatal("legitimate refresh suppressed or replaced initial observation")
	}
}

func TestReconciliationExcludesStaleEvidence(t *testing.T) {
	ctx := context.Background()
	module := &eventModule{fn: func(e runstate.Event) json.RawMessage {
		if e.Source.ID == "stale" {
			return json.RawMessage(`{"status":"error"}`)
		}
		return json.RawMessage(`{"status":"ignored"}`)
	}}
	p, _ := toolsHarness(t, []runstate.Definition{{Name: "world", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{"setting":"enabled"}`), Module: module}}, false)
	plan := runstate.Plan{Description: "Inspect system", Status: "active", Steps: []runstate.Step{{ID: "initial", Description: "Establish initial setting", Status: "satisfied", Outcome: "Initial setting established", Evidence: []runstate.EvidenceReference{{Partition: "world", Version: 0, Path: "/setting"}}}}}
	if err := p.Store.RevisePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	before := p.Store.Snapshot()
	if _, err := p.Store.Admit(ctx, runstate.Source{Kind: "user", ID: "stale"}, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	p.Reconciler = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		var input reconciliationInput
		_ = json.Unmarshal(raw, &input)
		if len(input.AcceptedEvidence) != 0 || len(input.EvidenceState) != 1 || input.EvidenceState[0].Freshness != "stale" {
			t.Fatal("stale state offered as fresh authoritative evidence")
		}
		return reconciliationResult(t, "no_progress", []progressUpdate{}, ""), nil
	}}
	if err := p.reconcile(ctx, before, "Revisit current setting", nil); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	if active.Plan.Steps[0].Status != "satisfied" {
		t.Fatal("staleness mechanically erased historical initial-setting outcome")
	}
}
