package state

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAttemptsCaptureDispatchAndPreserveStreakAcrossReplan(t *testing.T) {
	s := planStore(t)
	ctx := context.Background()
	plan := Plan{Description: "Inspect", Status: "active", Steps: []Step{{ID: "inspect", Description: "Inspect configuration", Status: "partial", Outcome: "Setting acquired", Evidence: []EvidenceReference{{Partition: "world", Path: "/setting"}}, InformationGaps: []string{"Assessment remains"}}}}
	if err := s.RevisePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= ReplanAfterNoProgress; i++ {
		active, _ := s.Active()
		ids, err := s.QueueAgentWork(ctx, []AgentWork{{OperationID: "op", Turn: 1, CallID: "call", Server: "test", Tool: "inspect", Arguments: json.RawMessage(`{}`)}})
		if err != nil {
			t.Fatal(err)
		}
		queued, _ := s.Active()
		if queued.Attempts.Total != uint64(i) || queued.Attempts.StepID != "inspect" || queued.Attempts.PlanRevision != active.Plan.Revision || queued.Attempts.ResultIDs[0] != ids[0] || queued.Attempts.InformationGaps[0] != "Assessment remains" {
			t.Fatal("dispatch attribution missing")
		}
		s.FinishAttempt(active.ID, false, "No additional assessment supported")
		s.FinishAttempt(active.ID, false, "Duplicate reconciliation must not count")
		// Complete the synthetic child so the next batch remains valid.
		if err := s.ActivateAgentWork(ctx, ids[0]); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordAgentOutcome(ctx, json.RawMessage(`{}`), "mcp_operation_failed"); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishAgentWork(ctx); err != nil {
			t.Fatal(err)
		}
	}
	active, _ := s.Active()
	if !active.Attempts.ReplanRequired || active.Attempts.ConsecutiveNoProgress != ReplanAfterNoProgress || !strings.Contains(string(s.ModelJSON()), `"replan_required":true`) {
		t.Fatal("replanning summary not exposed")
	}
	plan = *active.Plan
	plan.Steps = append(plan.Steps, Step{ID: "assessment", Description: "Revised assessment approach", Status: "pending"})
	plan.CurrentStep = "assessment"
	if err := s.RevisePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueueAgentWork(ctx, []AgentWork{{OperationID: "op", Turn: 1, CallID: "next", Server: "test", Tool: "inspect", Arguments: json.RawMessage(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	active, _ = s.Active()
	if !active.Attempts.ReplanRequired || active.Attempts.StepID != "assessment" || active.Attempts.Total != 4 || active.Plan.Steps[0].Outcome != "Setting acquired" {
		t.Fatal("replan erased history or collected progress")
	}
	s.FinishAttempt(active.ID, true, "Assessment established")
	active, _ = s.Active()
	if active.Attempts.ReplanRequired || active.Attempts.ConsecutiveNoProgress != 0 || active.Attempts.Outcome != "progress" {
		t.Fatal("accepted progress did not reset streak")
	}
}
