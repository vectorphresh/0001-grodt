package loop

import runstate "github.com/vectorphresh/0001-grodt/internal/state"

func planHasUnresolvedSteps(plan *runstate.Plan) bool {
	if plan != nil {
		for _, step := range plan.Steps {
			switch step.Status {
			case "pending", "active", "partial":
				return true
			}
		}
	}
	return false
}

func planRequiredMessage(plan *runstate.Plan) string {
	if plan == nil || len(plan.Steps) == 0 {
		return "Before using other tools, write down the steps you intend to take. Submit the plan by itself."
	}
	for _, step := range plan.Steps {
		if step.Status != "satisfied" {
			return "Your current plan has no unfinished actionable steps, but the goal is not finished. Write down the next steps you intend to take and submit the plan by itself."
		}
	}
	return "You completed your current plan, but the goal is not finished. Write down the next steps you intend to take and submit the plan by itself."
}
