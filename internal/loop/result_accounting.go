package loop

import (
	"fmt"
	"strings"

	runstate "github.com/vectorphresh/0001-grodt/internal/state"
)

type acceptedExecutionResult struct {
	ResultID string `json:"result_id"`
	CallID   string `json:"call_id"`
	Action   string `json:"action"`
	IsError  bool   `json:"is_error"`
}

type resultAccounting struct {
	ResultID    string `json:"result_id"`
	StepID      string `json:"step_id"`
	Disposition string `json:"disposition"`
	Reason      string `json:"reason"`
}

func validateResultAccounting(input reconciliationInput, decision reconciliationDecision) error {
	if strings.TrimSpace(decision.Reason) == "" || len(decision.Reason) > 512 {
		return fmt.Errorf("reason must contain 1–512 UTF-8 bytes explaining supported progress or why retained step state remains accurate")
	}
	if len(input.AcceptedResults) == 0 && len(decision.ResultAccounting) != 0 {
		return fmt.Errorf("accepted_results is empty. Return result_accounting=[]; failed or rejected tool results are not accepted results. Retain independently supported step updates using accumulated accepted evidence")
	}
	results := map[string]bool{}
	for _, result := range input.AcceptedResults {
		results[result.ResultID] = true
	}
	steps := map[string]bool{}
	if input.Plan != nil {
		for _, step := range input.Plan.Steps {
			steps[step.ID] = true
		}
	}
	updated := map[string]runstate.Step{}
	for _, update := range decision.Updates {
		updated[update.Step.ID] = update.Step
	}
	seen := map[string]bool{}
	for _, entry := range decision.ResultAccounting {
		if !results[entry.ResultID] {
			return fmt.Errorf("result_accounting references unknown result_id %q. Use only IDs from accepted_results", boundedPublicText(entry.ResultID, 128))
		}
		if seen[entry.ResultID] {
			return fmt.Errorf("result %q appears more than once. Keep one accounting entry for this result, associated with the work it contributes to. Retain other supported step updates; multiple steps may cite the same evidence without duplicate accounting", entry.ResultID)
		}
		seen[entry.ResultID] = true
		if !steps[entry.StepID] {
			return fmt.Errorf("result %q step_id %q is not visible accepted work. Select an ID in input.plan.steps", entry.ResultID, boundedPublicText(entry.StepID, 64))
		}
		if strings.TrimSpace(entry.Reason) == "" || len(entry.Reason) > 512 {
			return fmt.Errorf("result %q requires a 1–512 byte reason relating this observation to step %q", entry.ResultID, entry.StepID)
		}
		switch entry.Disposition {
		case "update":
			step, ok := updated[entry.StepID]
			if !ok || decision.Action != "progress" {
				return fmt.Errorf("result %q claims update for step %q but no matching step update exists. Use action progress and include that step's supported outcome, evidence and remaining gaps", entry.ResultID, entry.StepID)
			}
			if strings.TrimSpace(step.Outcome) == "" || len(step.Evidence) == 0 {
				return fmt.Errorf("result %q step update %q requires a supported outcome and accepted evidence_refs. If no additional progress is supported, use no_progress accounting with a specific reason", entry.ResultID, entry.StepID)
			}
		case "no_progress":
			if _, ok := updated[entry.StepID]; ok {
				return fmt.Errorf("result %q claims unchanged step %q but that step is updated. Use update accounting for contributed progress or identify the distinct unchanged accepted step", entry.ResultID, entry.StepID)
			}
		default:
			return fmt.Errorf("result %q disposition must be update or no_progress", entry.ResultID)
		}
	}
	for _, result := range input.AcceptedResults {
		if !seen[result.ResultID] {
			return fmt.Errorf("accepted result %q (%s) has no accounting. Include exactly one result_accounting entry with an accepted step_id, update or no_progress disposition, and a reason", result.ResultID, result.Action)
		}
	}
	return nil
}
