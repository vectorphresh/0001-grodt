package loop

import (
	"strings"
	"testing"

	runstate "github.com/vectorphresh/0001-grodt/internal/state"
)

func TestResultAccountingBaselineIsNotGoalCompletion(t *testing.T) {
	input := reconciliationInput{
		AcceptedResults: []acceptedExecutionResult{{ResultID: "account_result"}},
		Plan:            &runstate.Plan{Steps: []runstate.Step{{ID: "step_1", Status: "active"}}},
	}
	// Reproduce the a5be875 run: a baseline evidence update with an empty
	// outcome is incorrectly accounted as no_progress because no profit exists.
	d := reconciliationDecision{
		Action: "progress", Reason: "No profit verified yet", CurrentStep: "step_1",
		Updates:          []progressUpdate{{Step: runstate.Step{ID: "step_1", Status: "active", Evidence: []runstate.EvidenceReference{{Partition: "mcp_tools", Version: 1, Path: "/sources/alpaca/get_account_info/result"}}}}},
		ResultAccounting: []resultAccounting{{ResultID: "account_result", StepID: "step_1", Disposition: "no_progress", Reason: "No profit verified yet"}},
	}
	err := validateResultAccounting(input, d)
	if err == nil {
		t.Fatal("contradictory accounting accepted")
	}
	for _, want := range []string{"not achievement of the overall objective or profit", "disposition=update", "nonempty outcome", "remove this step from updates", "objective incomplete"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing correction %q: %v", want, err)
		}
	}
	d.ResultAccounting[0].Disposition = "update"
	d.Updates[0].Step.Outcome = "Account baseline acquired; profit has not been verified"
	if err := validateResultAccounting(input, d); err != nil {
		t.Fatal(err)
	}
	if d.Updates[0].Step.Status != "active" {
		t.Fatal("repair changed completion")
	}
	d.Updates = nil
	d.ResultAccounting[0].Disposition = "no_progress"
	d.Action, d.CurrentStep = "no_progress", ""
	if err := validateResultAccounting(input, d); err != nil {
		t.Fatal(err)
	}
}

func TestResultAccountingActionCorrection(t *testing.T) {
	input := reconciliationInput{
		AcceptedResults: []acceptedExecutionResult{{ResultID: "result"}},
		Plan:            &runstate.Plan{Steps: []runstate.Step{{ID: "step_1"}}},
	}
	update := progressUpdate{Step: runstate.Step{
		ID: "step_1", Status: "active", Outcome: "Assets acquired",
		Evidence: []runstate.EvidenceReference{{Partition: "mcp_tools", Version: 1}},
	}}
	for _, withUpdate := range []bool{false, true} {
		name := "without_matching_update"
		if withUpdate {
			name = "with_matching_update"
		}
		t.Run(name, func(t *testing.T) {
			d := reconciliationDecision{
				Action: "no_progress", Reason: "Assets acquired",
				ResultAccounting: []resultAccounting{{ResultID: "result", StepID: "step_1", Disposition: "update", Reason: "Assets acquired"}},
			}
			if withUpdate {
				d.Updates = []progressUpdate{update}
			}
			err := validateResultAccounting(input, d)
			if err == nil {
				t.Fatal("inconsistent action accepted")
			}
			feedback := reconciliationHostCorrection(err, mustEncode(d))
			for _, want := range []string{"update accounting requires action=progress", "Preserve any supported matching step update", "disposition=no_progress", "updates=[]", `current_step=""`, "unresolved_focus=null"} {
				if !strings.Contains(feedback, want) {
					t.Fatalf("missing actionable correction %q: %s", want, feedback)
				}
			}
			if strings.Contains(err.Error(), "no matching step update exists") {
				t.Fatal("action error misreported as missing update")
			}
			if withUpdate {
				// Repair only the action: supported candidate progress is retained.
				d.Action = "progress"
			} else {
				d.ResultAccounting[0].Disposition = "no_progress"
			}
			if err := validateResultAccounting(input, d); err != nil {
				t.Fatalf("suggested repair rejected: %v", err)
			}
		})
	}
}

func TestResultAccountingMissingUpdateCorrection(t *testing.T) {
	input := reconciliationInput{
		AcceptedResults: []acceptedExecutionResult{{ResultID: "result"}},
		Plan:            &runstate.Plan{Steps: []runstate.Step{{ID: "step_1"}}},
	}
	d := reconciliationDecision{
		Action: "progress", Reason: "Focus changed", CurrentStep: "step_1",
		ResultAccounting: []resultAccounting{{ResultID: "result", StepID: "step_1", Disposition: "update", Reason: "Assets acquired"}},
	}
	err := validateResultAccounting(input, d)
	if err == nil || !strings.Contains(err.Error(), "no matching step update exists") || !strings.Contains(err.Error(), "disposition=no_progress") {
		t.Fatalf("missing-update feedback: %v", err)
	}
	// An unchanged result can coexist with a separate procedural change.
	d.ResultAccounting[0].Disposition = "no_progress"
	if err := validateResultAccounting(input, d); err != nil {
		t.Fatal(err)
	}
}
