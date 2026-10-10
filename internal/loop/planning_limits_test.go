package loop

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

func TestPlanningAcceptanceResetsRejectionAllowance(t *testing.T) {
	for _, scenario := range []string{"many_acceptances", "acceptance_resets", "rejections_exhaust", "pending_external_does_not_bypass"} {
		t.Run(scenario, func(t *testing.T) {
			p, fixture := toolsHarness(t, nil, false)
			var operations []string
			appendRejects := func(n int) {
				for i := 0; i < n; i++ {
					operations = append(operations, "reject")
				}
			}
			expectedAcceptances := 0
			expectFailure := false
			switch scenario {
			case "many_acceptances":
				for i := 0; i < 40; i++ {
					operations = append(operations, "accept")
				}
				expectedAcceptances = 40
			case "acceptance_resets":
				for i := 0; i < 4; i++ {
					appendRejects(9)
					operations = append(operations, "accept")
				}
				expectedAcceptances = 4
			case "rejections_exhaust":
				appendRejects(maxHostCorrectionResponses)
				expectFailure = true
			case "pending_external_does_not_bypass":
				appendRejects(1)
				for i := 0; i < maxHostCorrectionResponses-1; i++ {
					operations = append(operations, "external")
				}
				expectFailure = true
			}
			turns := 0
			p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
				turns++
				if turns > len(operations) {
					return toolcall.Response{Text: "Finished planning."}, nil
				}
				op := operations[turns-1]
				if op == "external" {
					return toolcall.Response{Calls: []toolcall.Call{toolRequest(r, "read")}}, nil
				}
				plan := initialCorrectionPlan()
				active, _ := p.Store.Active()
				if active.Plan != nil {
					plan.Revision = active.Plan.Revision
				}
				if op == "reject" {
					plan.Steps[0].Status = "satisfied"
				} else {
					plan.UnresolvedFocus = "Actor purpose"
				}
				return planCall(t, fmt.Sprintf("plan-%d", turns), planCommand{Action: "revise", Plan: &plan}), nil
			})
			_, err := p.Handle(context.Background(), &State{Objective: "Inspect"})
			if expectFailure {
				if err == nil || !strings.Contains(err.Error(), "exclusive host correction exhausted") || strings.Contains(err.Error(), "preflight") || turns != len(operations) {
					t.Fatalf("incorrect exhaustion: err=%v turns=%d", err, turns)
				}
			} else if err != nil || turns != len(operations)+1 {
				t.Fatalf("successful planning exhausted: err=%v turns=%d", err, turns)
			}
			active, _ := p.Store.Active()
			if expectedAcceptances > 0 && (active.Plan == nil || active.Plan.Revision != uint64(expectedAcceptances)) {
				t.Fatalf("accepted plan missing: %+v", active.Plan)
			}
			if expectedAcceptances == 0 && active.Plan != nil {
				t.Fatal("rejected proposal persisted")
			}
			expectedCalls := int64(0)

			if fixture.Calls.Load() != expectedCalls {
				t.Fatal("unexpected dispatch")
			}
		})
	}
}
