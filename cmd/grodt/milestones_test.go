package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
)

func milestoneStore(t *testing.T) *runstate.Store {
	t.Helper()
	s, err := runstate.New(context.Background(), []runstate.Definition{{Name: "world", Schema: json.RawMessage(`true`), Initial: json.RawMessage(`{"orders":[]}`), Module: &stateModule{}}}, runstate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	return s
}

func TestMilestonesAcceptedTransitionsAndTaskScope(t *testing.T) {
	ctx := context.Background()
	s := milestoneStore(t)
	root, err := s.Push(ctx, "Inspect", "request")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	r := &milestoneReporter{store: s, status: &terminalStatus{writer: &output}}
	plan := runstate.Plan{Description: "Inspect account", Status: "active", Steps: []runstate.Step{{ID: "inspect", Description: "Inspect orders", Status: "active"}}, CurrentStep: "inspect", UnresolvedFocus: "Evaluate opportunities"}
	revise := func(p runstate.Plan) {
		t.Helper()
		if err := s.RevisePlan(ctx, p); err != nil {
			t.Fatal(err)
		}
		if err := r.drain(); err != nil {
			t.Fatal(err)
		}
	}
	revise(plan)
	if !strings.Contains(output.String(), root+": Plan accepted") || !strings.Contains(output.String(), "Evaluate opportunities") {
		t.Fatal(output.String())
	}
	output.Reset()
	active, _ := s.Active()
	plan = *active.Plan
	revise(plan) // Revision alone is not a new milestone.
	if output.Len() != 0 {
		t.Fatal("unchanged revision logged", output.String())
	}
	active, _ = s.Active()
	plan = *active.Plan
	plan.Steps[0].Status = "satisfied"
	plan.Steps[0].Outcome = "No orders"
	plan.Steps[0].Evidence = []runstate.EvidenceReference{{Partition: "world", Version: 0, Path: "/orders"}}
	plan.CurrentStep = ""
	revise(plan)
	if strings.Count(output.String(), "Step satisfied") != 1 || !strings.Contains(output.String(), "Current step: cleared") {
		t.Fatal(output.String())
	}
	output.Reset()
	active, _ = s.Active()
	plan = *active.Plan
	plan.UnresolvedFocus = ""
	revise(plan)
	if !strings.Contains(output.String(), "Unresolved focus: cleared") || strings.Contains(output.String(), "Step satisfied") {
		t.Fatal(output.String())
	}
	output.Reset()
	active, _ = s.Active()
	plan = *active.Plan
	plan.Steps[0].Status = "invalidated"
	revise(plan)
	if !strings.Contains(output.String(), "Step invalidated") {
		t.Fatal(output.String())
	}
	child, err := s.Push(ctx, "Child", "request")
	if err != nil {
		t.Fatal(err)
	}
	output.Reset()
	plan.Revision = 0
	revise(plan)
	if !strings.Contains(output.String(), child+": Plan accepted") || !strings.Contains(output.String(), "Step invalidated") {
		t.Fatal("tasks conflated", output.String())
	}
	output.Reset()
	if err := r.drain(); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatal("duplicate output")
	}
}

func TestMilestonesRejectionsRecoveryBoundsAndRedaction(t *testing.T) {
	s := milestoneStore(t)
	var output bytes.Buffer
	r := &milestoneReporter{store: s, status: &terminalStatus{writer: &output, key: "secret"}}
	// Rejected/evaluated proposals are not accepted progress; large bodies stay excluded.
	s.RecordPlanEvaluation(json.RawMessage(`{"status":"satisfied","payload":"`+strings.Repeat("x", 1<<20)+`"}`), json.RawMessage(`{"satisfied":false}`))
	s.Diagnostic("exclusive_host_correction_started")
	s.Diagnostic("exclusive_host_correction_rejected", 1)
	s.Diagnostic("exclusive_host_correction_rejected", 10)
	s.Diagnostic("planning_correction_exhausted")
	s.Diagnostic("reconciliation_correction_requested", 2)
	s.RecordToolRecovery(json.RawMessage(`{"category":"result_too_large","execution":"result_rejected","details":{"observed_bytes":1200000,"limit_bytes":1048576,"untrusted":123},"validation_keywords":["type"]}`))
	if err := r.drain(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"no calls dispatched", "Attempt 1", "Attempt 2", "Attempt 10.", "32 rejections since the last acceptance", "1200000", "1048576", "validation=type"} {
		if !strings.Contains(output.String(), want) {
			t.Fatal(want, output.String())
		}
	}
	if strings.Contains(output.String(), "Step satisfied") || strings.Contains(output.String(), "untrusted") || output.Len() > 1500 {
		t.Fatal(output.String())
	}
	_, err := s.Push(context.Background(), "Goal", "request")
	if err != nil {
		t.Fatal(err)
	}
	plan := runstate.Plan{Description: "Plan", Status: "active", Steps: []runstate.Step{{ID: "inspect", Description: "Inspect", Status: "pending"}}, UnresolvedFocus: "secret\n\x1b" + strings.Repeat("界", 160)}
	if err := s.RevisePlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := r.drain(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "secret") || strings.Contains(output.String(), "\x1b") || !strings.Contains(output.String(), "[redacted]") || !strings.Contains(output.String(), "…") {
		t.Fatal(output.String())
	}
	r.status.writer = brokenWriter{}
	s.Diagnostic("exclusive_host_correction_exhausted")
	if err := r.drain(); err == nil {
		t.Fatal("writer failure swallowed")
	}
	r.status.writer = &output
	if err := r.drain(); err == nil {
		t.Fatal("writer failure must remain terminal")
	}
}

func TestMilestoneRedactionBeforeTruncation(t *testing.T) {
	r := &milestoneReporter{status: &terminalStatus{key: strings.Repeat("s", 200)}}
	text := r.text(strings.Repeat("a", 150) + r.status.key)
	if strings.Contains(text, "ssss") {
		t.Fatal("credential split by truncation", text)
	}
}

func TestPlanningStageMilestones(t *testing.T) {
	s := milestoneStore(t)
	var output bytes.Buffer
	r := &milestoneReporter{store: s, status: &terminalStatus{writer: &output, key: "secret"}}
	s.RecordPlanningStage("task", "revise", "started", "")
	if err := r.drain(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Planning started: revise.") || strings.Contains(output.String(), "completed") {
		t.Fatal(output.String())
	}
	s.RecordPlanningStage("task", "revise", "rejected", "secret\n\x1b"+strings.Repeat("x", 200))
	s.RecordPlanningStage("task", "create_child", "started", "")
	s.RecordPlanningStage("task", "create_child", "accepted", "")
	if err := r.drain(); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"Planning completed: rejected (revise)", "[redacted]", "…", "Planning started: create_child.", "Planning completed: accepted (create_child)"} {
		if !strings.Contains(text, want) {
			t.Fatal(want, text)
		}
	}
	if strings.Contains(text, "secret") || strings.Contains(text, "\x1b") || strings.Count(text, "\n") != 4 {
		t.Fatal("unbounded or multiline stage output", text)
	}
	output.Reset()
	if err := r.drain(); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatal("stage logged twice")
	}
}

func TestMilestoneWriterFailureStopsInference(t *testing.T) {
	s := milestoneStore(t)
	s.Diagnostic("exclusive_host_correction_started")
	called := false
	c := observedClient{Client: testClient{prompt: func(context.Context, string) (openai.TextResult, error) {
		called = true
		return openai.TextResult{}, nil
	}}, metrics: &runMetrics{}, milestones: &milestoneReporter{store: s, status: &terminalStatus{writer: brokenWriter{}}}}
	if _, err := c.Prompt(context.Background(), "request"); err == nil || called {
		t.Fatal("inference proceeded after diagnostic failure")
	}
}

func TestMilestonesFlushBeforeNextRequestAndFinalReport(t *testing.T) {
	for _, final := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_request", true: "final_report"}[final], func(t *testing.T) {
			s := milestoneStore(t)
			var diagnostics bytes.Buffer
			c := testClient{prompt: func(context.Context, string) (openai.TextResult, error) {
				return openai.TextResult{Text: "answer"}, nil
			}, mutation: func(context.Context, string, json.RawMessage, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
				if final {
					s.Diagnostic("exclusive_host_correction_exhausted")
				}
				return evaluationResult(true, "done"), nil
			}}
			if !final {
				s.Diagnostic("exclusive_host_correction_started")
			}
			if err := executeWithState(context.Background(), "Goal", "request", c, io.Discard, &terminalStatus{writer: &diagnostics}, s); err != nil {
				t.Fatal(err)
			}
			want, boundary := "Planning correction requested", "Generation request in progress"
			if final {
				want, boundary = "Planning correction exhausted", "Objective:"
			}
			text := diagnostics.String()
			if strings.Count(text, want) != 1 || strings.Index(text, want) > strings.Index(text, boundary) {
				t.Fatal(text)
			}
		})
	}
}
