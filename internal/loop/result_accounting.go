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
			if decision.Action != "progress" {
				return fmt.Errorf("result %q has disposition=update but action=%q; update accounting requires action=progress. Preserve any supported matching step update and set action=progress. If no additional progress is supported, set disposition=no_progress with a reason, updates=[], current_step=\"\", and unresolved_focus=null", entry.ResultID, decision.Action)
			}
			if !ok {
				return fmt.Errorf("result %q claims update for step %q but no matching step update exists. Include an updates entry for this step with its supported outcome, evidence_refs and remaining gaps. If the step is unchanged, set this accounting entry's disposition=no_progress with a reason; if no procedural changes are needed, also set action=no_progress, updates=[], current_step=\"\", and unresolved_focus=null", entry.ResultID, entry.StepID)
			}
			if strings.TrimSpace(step.Outcome) == "" || len(step.Evidence) == 0 {
				return fmt.Errorf("result %q step update %q requires a supported outcome and accepted evidence_refs. If no additional progress is supported, use no_progress accounting with a specific reason", entry.ResultID, entry.StepID)
			}
		case "no_progress":
			if _, ok := updated[entry.StepID]; ok {
				return fmt.Errorf("result %q claims unchanged step %q but that step is updated. Result accounting describes contribution to a step, not achievement of the overall objective or profit. Choose one consistent repair: (1) preserve the supported step update, set this result_accounting entry's disposition=update, and provide a nonempty outcome describing what the evidence establishes; the step may remain active and the objective incomplete; or (2) if there is no additional supported progress, remove this step from updates and retain disposition=no_progress with a reason. If no other procedural changes are needed, also set action=no_progress, updates=[], current_step=\"\", and unresolved_focus=null. Do not return the same combination of a step update and no_progress accounting for that step", entry.ResultID, entry.StepID)
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
