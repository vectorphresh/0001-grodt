package state

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func planStore(t *testing.T) *Store {
	t.Helper()
	module := &processor{fn: func(_ json.RawMessage, e Event) (json.RawMessage, error) {
		if string(e.Payload) == `"change"` {
			return json.RawMessage(`{"status":"mutation","replace":{"setting":"new"}}`), nil
		}
		return json.RawMessage(`{"status":"ignored"}`), nil
	}}
	s := newStore(t, Definition{Name: "world", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{"setting":"enabled","a/b":{"~name":12345678901234567890}}`), Module: module})
	if _, err := s.Push(context.Background(), "Determine system configuration", "raw request archive"); err != nil {
		t.Fatal(err)
	}
	return s
}
func TestPlanContinuityRevisionAndInvalidation(t *testing.T) {
	ctx := context.Background()
	s := planStore(t)
	plan := Plan{Description: "Determine configuration", Status: "active", Steps: []Step{{ID: "configuration", Description: "Obtain configuration", CompletionCriteria: "Accepted setting is available", Status: "satisfied", Outcome: "Setting established as enabled", Evidence: []EvidenceReference{{Partition: "world", Version: 0, Path: "/setting"}}}, {ID: "monitor", Description: "Observe future changes", Status: "active", InformationGaps: []string{"Future condition unknown"}}}, CurrentStep: "monitor"}
	if err := s.RevisePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	plan.Steps[0].Outcome = "caller mutation"
	active, _ := s.Active()
	if active.Plan.Steps[0].Outcome == "caller mutation" {
		t.Fatal("caller mutated accepted state")
	}
	before := s.ModelJSON()
	for i := 0; i < 50; i++ {
		if err := s.BeginCycle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	after := s.ModelJSON()
	if len(after) > len(before)+24 || !bytes.Contains(after, []byte("Setting established as enabled")) || bytes.Contains(after, []byte("raw request archive")) {
		t.Fatal("continuity or projection bound failed")
	}
	if _, err := s.Admit(ctx, Source{Kind: "user", ID: "change"}, json.RawMessage(`"change"`)); err != nil {
		t.Fatal(err)
	}
	active, _ = s.Active()
	next := *active.Plan
	next.Steps[1].Description = "Reassess conditions"
	if err := s.RevisePlan(ctx, next); err != nil {
		t.Fatal("historical satisfied evidence must survive fresh observations:", err)
	}
	active, _ = s.Active()
	next = *active.Plan
	next.Steps[0].Status = "invalidated"
	if err := s.RevisePlan(ctx, next); err != nil {
		t.Fatal(err)
	}
	journal := string(encode(s.Journal()))
	if !strings.Contains(journal, `"status":"satisfied"`) || !strings.Contains(journal, `"status":"invalidated"`) || !strings.Contains(journal, "task_plan_revised") {
		t.Fatal("invalidation erased history")
	}
	active, _ = s.Active()
	next = *active.Plan
	next.Steps = next.Steps[1:]
	snapshot := s.JSON()
	if err := s.RevisePlan(ctx, next); err == nil || !bytes.Equal(snapshot, s.JSON()) {
		t.Fatal("completed history removed or rejected update mutated state")
	}
}
func TestPlanEvidenceAndStructuralValidation(t *testing.T) {
	ctx := context.Background()
	s := planStore(t)
	raw, err := s.ResolveEvidence(EvidenceReference{Partition: "world", Version: 0, Path: "/a~1b/~0name"})
	if err != nil || string(raw) != "12345678901234567890" {
		t.Fatalf("evidence changed: %s %v", raw, err)
	}
	for _, ref := range []EvidenceReference{{Partition: "absent"}, {Partition: "world", Version: 9}, {Partition: "world", Path: "/absent"}, {Partition: "world", Path: "/bad~"}} {
		if _, err := s.ResolveEvidence(ref); err == nil {
			t.Fatal("fabricated evidence accepted")
		}
	}
	base := Plan{Description: "Intent", Status: "active", Steps: []Step{{ID: "inspect", Description: "Inspect", Status: "active"}}, CurrentStep: "inspect"}
	if err := s.RevisePlan(ctx, base); err != nil {
		t.Fatal(err)
	}
	if err := s.RevisePlan(ctx, base); err == nil {
		t.Fatal("stale plan revision accepted")
	}
	active, _ := s.Active()
	bad := *active.Plan
	bad.Steps = append(bad.Steps, bad.Steps[0])
	if err := s.RevisePlan(ctx, bad); err == nil {
		t.Fatal("duplicate step accepted")
	}
	active, _ = s.Active()
	bad = *active.Plan
	bad.Steps[0].Status = "satisfied"
	if err := s.RevisePlan(ctx, bad); err == nil {
		t.Fatal("activity without evidence accepted")
	}
}

func TestCollectedStepEvidenceRequiresStartedStatus(t *testing.T) {
	ctx := context.Background()
	s := planStore(t)
	plan := Plan{Description: "Inspect configuration", Status: "active", Steps: []Step{{ID: "inspect", Description: "Inspect configuration", Status: "pending"}}}
	if err := s.RevisePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	active, _ := s.Active()
	plan = *active.Plan
	plan.Steps[0].Evidence = []EvidenceReference{{Partition: "world", Version: 0, Path: "/setting"}}
	before := s.JSON()
	err := s.RevisePlan(ctx, plan)
	if err == nil || !strings.Contains(err.Error(), "cannot remain pending") || !strings.Contains(err.Error(), "active") || !bytes.Equal(before, s.JSON()) {
		t.Fatalf("pending evidence must be rejected atomically with actionable feedback: %v", err)
	}
	plan.Steps[0].Status = "active"
	if err := s.RevisePlan(ctx, plan); err != nil {
		t.Fatalf("started work with collected evidence rejected: %v", err)
	}
}

func TestRootCompletionDoesNotAutoSatisfyChildren(t *testing.T) {
	ctx := context.Background()
	s := planStore(t)
	child, err := s.Push(ctx, "Continue investigation", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevisePlan(ctx, Plan{Description: "Investigation", Status: "partial", InformationGaps: []string{"Unknown cause"}, Steps: []Step{{ID: "investigate", Description: "Investigate cause", Status: "partial"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteObjective(ctx, "Root outcome independently confirmed"); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	if len(snap.Tasks.Stack) != 0 || snap.Tasks.Records[child].Status != "incomplete" {
		t.Fatal("root completion left active child or fabricated child satisfaction")
	}
	for _, task := range snap.Tasks.Records {
		if task.ParentID == "" && task.Status != "completed" {
			t.Fatal("root not completed")
		}
	}
}

func TestPlanFocusCompatibilityBoundsAndCompletion(t *testing.T) {
	s := planStore(t)
	ctx := context.Background()
	var legacy Plan
	if err := json.Unmarshal([]byte(`{"description":"Legacy intent","status":"active","revision":0,"steps":[]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.UnresolvedFocus != "" {
		t.Fatal("legacy plan gained focus")
	}
	if err := s.RevisePlan(ctx, legacy); err == nil {
		t.Fatal("empty legacy plan accepted")
	}
	legacy.Steps = []Step{{ID: "inspect", Description: "Inspect", Status: "pending"}}
	if err := s.RevisePlan(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	active, _ := s.Active()
	next := *active.Plan
	next.UnresolvedFocus = strings.Repeat("x", 513)
	if err := s.RevisePlan(ctx, next); err == nil {
		t.Fatal("oversized focus accepted")
	}
	next.UnresolvedFocus = "Evaluate remaining information"
	if err := s.RevisePlan(ctx, next); err != nil {
		t.Fatal(err)
	}
	active, _ = s.Active()
	next = *active.Plan
	next.Status = "satisfied"
	next.Outcome = "Complete"
	next.Evidence = []EvidenceReference{{Partition: "world", Path: "/setting"}}
	if err := s.CheckPlan(next); err == nil {
		t.Fatal("plan satisfied with unresolved focus")
	}
	next.UnresolvedFocus = ""
	next.Steps = append([]Step(nil), next.Steps...)
	next.Steps[0].Status = "satisfied"
	next.Steps[0].Outcome = "Inspected"
	next.Steps[0].Evidence = next.Evidence
	next.CurrentStep = ""
	if err := s.CheckPlan(next); err != nil {
		t.Fatal(err)
	}
}

func TestOrderedCurrentPositionAndEmptyPlan(t *testing.T) {
	ctx := context.Background()
	s := planStore(t)
	if err := s.RevisePlan(ctx, Plan{Description: "Empty", Status: "active", Steps: []Step{}}); err == nil {
		t.Fatal("accepted empty plan")
	}
	plan := Plan{Description: "Inspect", Status: "active", Steps: []Step{{ID: "a", Description: "Inspect account", Status: "pending"}, {ID: "b", Description: "Assess quotes", Status: "pending"}, {ID: "c", Description: "Assess setup", Status: "pending"}}}
	if err := s.RevisePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	active, _ := s.Active()
	if active.Plan.CurrentStep != "a" {
		t.Fatal("initial position not derived")
	}
	plan = *active.Plan
	plan.Steps = append([]Step(nil), plan.Steps...)
	plan.Steps[0].Status = "failed"
	plan.CurrentStep = ""
	if err := s.RevisePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	active, _ = s.Active()
	if active.Plan.CurrentStep != "b" || len(active.Plan.Steps) != 3 {
		t.Fatal("ordered advancement lost steps")
	}
	var view struct {
		Tasks struct {
			Records map[string]struct {
				CurrentPosition int   `json:"current_position"`
				Plan            *Plan `json:"plan"`
			} `json:"records"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(s.ModelJSON(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Tasks.Records[active.ID].CurrentPosition != 2 || len(view.Tasks.Records[active.ID].Plan.Steps) != 3 {
		t.Fatal("projection lost authoritative position")
	}
}
