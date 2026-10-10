package loop

import (
	"encoding/json"
	"errors"
	"fmt"

	runstate "github.com/vectorphresh/0001-grodt/internal/state"
)

type planPatch struct {
	Revision        uint64            `json:"revision"`
	AddSteps        []runstate.Step   `json:"add_steps,omitempty"`
	StepUpdates     []json.RawMessage `json:"step_updates,omitempty"`
	RemoveSteps     []string          `json:"remove_steps,omitempty"`
	UnresolvedOrder []string          `json:"unresolved_order,omitempty"`
	PlanUpdates     json.RawMessage   `json:"plan_updates,omitempty"`
}

func requireSnapshotSteps(old, next *runstate.Plan) error {
	if old == nil {
		return nil
	}
	ids := map[string]bool{}
	for _, step := range next.Steps {
		ids[step.ID] = true
	}
	for _, step := range old.Steps {
		if !ids[step.ID] {
			return fmt.Errorf("revise omitted accepted step %s. Retain every accepted step, or use patch with explicit remove_steps and revision_reason", step.ID)
		}
	}
	return nil
}

// mergeFields retains fields absent from an actor patch, including evidence.
func mergeFields(target any, update json.RawMessage) error {
	raw, err := json.Marshal(target)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	var changes map[string]json.RawMessage
	if err = json.Unmarshal(update, &changes); err != nil {
		return err
	}
	for key, value := range changes {
		fields[key] = value
	}
	raw, err = json.Marshal(fields)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func applyPlanPatch(old *runstate.Plan, patch *planPatch) (runstate.Plan, error) {
	var plan runstate.Plan
	if old == nil || patch == nil {
		return plan, errors.New("patch requires an accepted plan; submit revise with at least one unfinished step")
	}
	if patch.Revision != old.Revision {
		return plan, fmt.Errorf("set patch.revision to accepted revision %d; rejected updates do not increment it", old.Revision)
	}
	raw, _ := json.Marshal(old)
	_ = json.Unmarshal(raw, &plan)
	if len(patch.AddSteps)+len(patch.StepUpdates)+len(patch.RemoveSteps) > 8 {
		return plan, errors.New("patch allows at most 8 step mutations")
	}
	if len(patch.AddSteps)+len(patch.StepUpdates)+len(patch.RemoveSteps) == 0 && patch.UnresolvedOrder == nil && len(patch.PlanUpdates) == 0 {
		return plan, errors.New("patch has no changes")
	}
	touched := map[string]bool{}
	touch := func(id string) error {
		if touched[id] {
			return fmt.Errorf("patch addresses step %s more than once", id)
		}
		touched[id] = true
		return nil
	}
	find := func(id string) int {
		for i, s := range plan.Steps {
			if s.ID == id {
				return i
			}
		}
		return -1
	}
	for _, id := range patch.RemoveSteps {
		if err := touch(id); err != nil {
			return plan, err
		}
		i := find(id)
		if i < 0 {
			return plan, fmt.Errorf("cannot remove unknown step %s", id)
		}
		if !runstate.StepUnresolved(plan.Steps[i]) {
			return plan, errors.New("cannot remove terminal step history")
		}
		plan.Steps = append(plan.Steps[:i], plan.Steps[i+1:]...)
	}
	for _, update := range patch.StepUpdates {
		var identity struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(update, &identity); err != nil {
			return plan, err
		}
		if err := touch(identity.ID); err != nil {
			return plan, err
		}
		i := find(identity.ID)
		if i < 0 {
			return plan, fmt.Errorf("cannot update unknown step %s", identity.ID)
		}
		if err := mergeFields(&plan.Steps[i], update); err != nil {
			return plan, err
		}
	}
	for _, step := range patch.AddSteps {
		if err := touch(step.ID); err != nil {
			return plan, err
		}
		if find(step.ID) >= 0 {
			return plan, fmt.Errorf("step %s already exists", step.ID)
		}
		plan.Steps = append(plan.Steps, step)
	}
	if patch.UnresolvedOrder != nil {
		unresolved := map[string]runstate.Step{}
		for _, step := range plan.Steps {
			if runstate.StepUnresolved(step) {
				unresolved[step.ID] = step
			}
		}
		if len(unresolved) != len(patch.UnresolvedOrder) {
			return plan, errors.New("unresolved_order must list every unresolved step exactly once")
		}
		ordered := []runstate.Step{}
		for _, id := range patch.UnresolvedOrder {
			step, ok := unresolved[id]
			if !ok {
				return plan, errors.New("unresolved_order contains duplicate, terminal or unknown ID")
			}
			ordered = append(ordered, step)
			delete(unresolved, id)
		}
		j := 0
		for i, step := range plan.Steps {
			if runstate.StepUnresolved(step) {
				plan.Steps[i] = ordered[j]
				j++
			}
		}
	}
	explicitCurrent := false
	if len(patch.PlanUpdates) > 0 {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(patch.PlanUpdates, &fields)
		_, explicitCurrent = fields["current_step"]
		if err := mergeFields(&plan, patch.PlanUpdates); err != nil {
			return plan, err
		}
	}
	if len(plan.Steps) == 0 {
		return plan, errors.New("your plan has no steps; add at least one unfinished step and submit the plan by itself")
	}
	if explicitCurrent {
		valid := false
		for _, step := range plan.Steps {
			if step.ID == plan.CurrentStep && runstate.StepUnresolved(step) {
				valid = true
			}
		}
		if !valid {
			return plan, errors.New("current_step must select an unresolved step")
		}
	} else {
		plan.AdvanceCurrentStep()
	}
	return plan, nil
}

func (p *ToolProvider) commandPlan(cmd planCommand) (*runstate.Plan, error) {
	if cmd.Action != "patch" {
		return cmd.Plan, nil
	}
	active, _ := p.Store.Active()
	plan, err := applyPlanPatch(active.Plan, cmd.Patch)
	if err != nil {
		return nil, err
	}
	return &plan, nil
}
