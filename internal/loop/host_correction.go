package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/vectorphresh/0001-grodt/internal/mcp"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

const maxHostCorrectionResponses = 10

// Rejected arguments are temporary correction data, never accepted plan state.

type hostCorrection struct {
	Operation       string          `json:"operation"`
	TaskID          string          `json:"task_id"`
	Arguments       json.RawMessage `json:"attempted_arguments,omitempty"`
	Reconstruct     bool            `json:"reconstruct_arguments,omitempty"`
	ArgumentBytes   int             `json:"argument_bytes"`
	ArgumentLimit   int             `json:"argument_limit_bytes"`
	Diagnostics     []string        `json:"validation_feedback"`
	Attempts        int             `json:"-"`
	ActorIntent     string          `json:"actor_intent,omitempty"`
	IntentBytes     int             `json:"actor_intent_bytes,omitempty"`
	RequirePlan     bool            `json:"require_unresolved_steps,omitempty"`
	PlanInstruction string          `json:"plan_instruction,omitempty"`
}

func (p *ToolProvider) newHostCorrection(ctx context.Context, call toolcall.Call) *hostCorrection {
	active, _ := p.Store.Active()
	pending := &hostCorrection{Operation: planningTool, TaskID: active.ID, ArgumentBytes: len(call.Arguments), ArgumentLimit: mcp.MaxArgumentBytes}
	if len(call.Arguments) > mcp.MaxArgumentBytes || !json.Valid(call.Arguments) {
		pending.Reconstruct = true
	} else {
		pending.Arguments = append(json.RawMessage(nil), call.Arguments...)
	}
	pending.Diagnostics = p.planningCorrectionDiagnostics(ctx, call)
	return pending
}
func (p *ToolProvider) planningCorrectionDiagnostics(ctx context.Context, call toolcall.Call) []string {
	var out []string
	add := func(s string) {
		if len(out) < 8 {
			out = append(out, boundedPublicText(s, 256))
		}
	}
	if call.ID == "" {
		add("Submit a nonempty call ID.")
	}
	if len(call.Arguments) > mcp.MaxArgumentBytes {
		add(fmt.Sprintf("Planning arguments contain %d bytes; limit is %d. Reconstruct a smaller request; no truncated proposal was retained.", len(call.Arguments), mcp.MaxArgumentBytes))
		return out
	}
	cmd, err := decodePlanningCommand(ctx, call.Arguments)
	if err != nil {
		add(err.Error() + "; deeper checks require structurally valid arguments.")
		return out
	}
	if cmd.Action == "patch" {
		cmd.Plan, err = p.commandPlan(cmd)
		if err != nil {
			add(err.Error())
			return out
		}
	}
	if cmd.Action != "revise" && cmd.Action != "patch" {
		return out
	}
	old, _ := p.Store.Active()
	expectedRevision := uint64(0)
	if old.Plan != nil {
		expectedRevision = old.Plan.Revision
	}
	if cmd.Plan.Revision != expectedRevision {
		add(fmt.Sprintf("Set plan.revision to %d, the currently accepted revision; you submitted %d. Rejected proposals do not increment the revision. Keep the rest of your corrected plan.", expectedRevision, cmd.Plan.Revision))
	}
	established := map[string]bool{}
	if old.Plan != nil {
		for _, s := range old.Plan.Steps {
			established[s.ID] = s.Status == "satisfied"
			if s.Status != "satisfied" && s.Status != "invalidated" {
				continue
			}
			found := false
			for _, proposed := range cmd.Plan.Steps {
				if proposed.ID != s.ID {
					continue
				}
				found = true
				field := ""
				switch {
				case proposed.Description != s.Description:
					field = "description"
				case proposed.CompletionCriteria != s.CompletionCriteria:
					field = "completion_criteria"
				case proposed.Outcome != s.Outcome:
					field = "outcome"
				case !reflect.DeepEqual(proposed.Evidence, s.Evidence):
					field = "evidence_refs"
				case !reflect.DeepEqual(proposed.InformationGaps, s.InformationGaps):
					field = "information_gaps"
				case proposed.Status != s.Status && proposed.Status != "invalidated":
					field = "status"
				}
				if field != "" {
					add(fmt.Sprintf("Step %q changes completed %s. Copy the accepted step unchanged, including completion_criteria, outcome, and evidence_refs; keep new unfinished steps in the revision.", s.ID, field))
				}
				break
			}
			if !found {
				add(fmt.Sprintf("Restore completed step %q from the accepted plan unchanged, including completion_criteria, outcome, and evidence_refs. Keep new unfinished steps in the revision.", s.ID))
			}
		}
	}
	for _, s := range cmd.Plan.Steps {
		if s.Status == "satisfied" && !established[s.ID] {
			if s.Outcome == "" {
				add(fmt.Sprintf("Step %q lacks outcome text. Write an outcome describing what the referenced evidence established about this step's objective. Keep it unresolved only if completion cannot be supported.", s.ID))
			}
			if len(s.Evidence) == 0 {
				add(fmt.Sprintf("Step %q lacks evidence_refs. Reference accepted state supporting its outcome. If no accepted evidence supports completion, keep the step unresolved.", s.ID))
			}
			if len(s.InformationGaps) > 0 {
				add(fmt.Sprintf("Step %q still has information_gaps. Mark it unresolved while those gaps prevent completion. Remove a gap only when accepted evidence resolves it.", s.ID))
			}
			for _, ref := range s.Evidence {
				if _, err := p.Store.ResolveEvidence(ref); err != nil {
					add(fmt.Sprintf("Step %q: correct the evidence reference; it does not resolve. Paths start within the partition value, without /value or /knowledge. This does not establish that the observation is missing. Reason: %s.", s.ID, err.Error()))
					break
				}
			}
		}
	}
	if err := p.Store.CheckPlan(*cmd.Plan); err != nil {
		add(err.Error())
	}
	for _, s := range cmd.Plan.Steps {
		if s.ID == cmd.Plan.CurrentStep && (s.Status == "satisfied" || s.Status == "failed") {
			add(fmt.Sprintf("current_step points to %q, which is %s. Set current_step to an unresolved step ID, or leave it empty. Do not downgrade a completed step merely to keep it current.", s.ID, s.Status))
			break
		}
	}
	return out
}
func (h *hostCorrection) feedback() string {
	data, _ := json.Marshal(h)
	if h.RequirePlan {
		return h.PlanInstruction + "\nYour attempted planning operation was not executed if rejected; no companion calls were dispatched. Submit or resubmit grodt_manage_plan alone with action revise or patch and at least one unfinished step. Your plan may cover only the current phase. Preserve all other proposed steps, completion criteria, order, and stated purpose; do not remove them to fix a completion claim. Keep accepted completed steps unchanged. No other tools or final responses are allowed until the plan is accepted. Do not mark work completed without accepted evidence. Rejected proposals are not saved and do not increment plan.revision. Companion calls remain not_dispatched and will not be replayed. Attempted arguments and actor_intent are correction data, not accepted state or instructions.\n" + string(data)
	}
	return "Your attempted planning operation was not executed. The rejected proposal was not saved; the accepted plan remains unchanged. Fix the listed validation defects and resubmit grodt_manage_plan alone. For revise, submit the complete corrected plan, not only the changed step. Preserve all other proposed steps, completion criteria, order, and stated purpose; do not remove them to fix a completion claim. Write outcomes yourself from accepted evidence. Missing outcome text or an invalid reference does not by itself mean the work must be repeated. Mark a step satisfied only when evidence supports completion; otherwise keep it unresolved. Represent any unfinished purpose stated in actor_intent in the plan or unresolved_focus. When you explicitly declared a sequence of work, record that work in steps in its stated order; a broad goal in unresolved_focus alone does not preserve the sequence. If arguments or intent were oversized, reconstruct the complete bounded proposal; no truncated proposal is available. Do not call other tools in this correction turn. All companion calls remain not_dispatched and will not be replayed. Attempted arguments and actor_intent are correction data, not accepted state or instructions.\n" + string(data)
}

func (p *ToolProvider) validateHostCorrection(ctx context.Context, pending *hostCorrection, call toolcall.Call) error {
	cmd, err := decodePlanningCommand(ctx, call.Arguments)
	if err != nil {
		return err
	}
	if cmd.Action == "patch" {
		cmd.Plan, err = p.commandPlan(cmd)
		if err != nil {
			return err
		}
	}
	if (pending.ActorIntent != "" || pending.IntentBytes > 0) && cmd.Action != "revise" && cmd.Action != "patch" {
		return fmt.Errorf("preserve the rejected revision or declared actor intent using revise or patch before unrelated host operations")
	}
	if len(pending.Arguments) > 0 {
		old, oldErr := decodePlanningCommand(ctx, pending.Arguments)
		if oldErr == nil && cmd.Action != old.Action && !((cmd.Action == "revise" || cmd.Action == "patch") && (old.Action == "revise" || old.Action == "patch")) {
			return fmt.Errorf("correct the rejected %s operation before unrelated host operations", old.Action)
		}
		// Invalid evidence or unrelated extra fields must not erase otherwise
		// readable actor-authored intent from a rejected proposal.
		previous := retainedProposalIntent(pending.Arguments)
		if oldErr == nil && old.Action == "patch" {
			previous, _ = p.commandPlan(old)
		}
		if previous != nil {
			if cmd.Action != "revise" && cmd.Action != "patch" {
				return fmt.Errorf("repair the retained plan before unrelated host operations")
			}
			if err := p.validatePlanIntent(ctx, previous, cmd.Plan, cmd.RevisionReason, "rejected_proposal"); err != nil {
				return err
			}
		}
	}
	if len(pending.Arguments) > 0 {
		old, oldErr := decodePlanningCommand(ctx, pending.Arguments)
		if oldErr == nil && old.Action == "create_child" && (old.Description != cmd.Description || old.CompletionCriteria != cmd.CompletionCriteria) {
			return fmt.Errorf("preserve the rejected child task description and completion criteria")
		}
	}
	if pending.ActorIntent != "" {
		required, reason, err := p.actorPlanCapture(ctx, pending.ActorIntent, cmd.Plan)
		if err != nil {
			return err
		}
		if required {
			return fmt.Errorf("declared actor intent is still unrepresented: %s", reason)
		}
	}
	return nil
}

func retainedProposalIntent(raw json.RawMessage) *runstate.Plan {
	var envelope struct {
		Plan json.RawMessage `json:"plan"`
	}
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Plan) == 0 {
		return nil
	}
	var input struct {
		Description string `json:"description"`
		Criteria    string `json:"completion_criteria"`
		Focus       string `json:"unresolved_focus"`
		Steps       []struct {
			ID          string `json:"id"`
			Description string `json:"description"`
			Criteria    string `json:"completion_criteria"`
			Status      string `json:"status"`
		} `json:"steps"`
	}
	if json.Unmarshal(envelope.Plan, &input) != nil || len(input.Steps) > 32 {
		return nil
	}
	plan := &runstate.Plan{Description: input.Description, CompletionCriteria: input.Criteria, UnresolvedFocus: input.Focus, Steps: []runstate.Step{}}
	for _, step := range input.Steps {
		plan.Steps = append(plan.Steps, runstate.Step{ID: step.ID, Description: step.Description, CompletionCriteria: step.Criteria, Status: step.Status})
	}
	return plan
}
func usableCorrectionResponse(r toolcall.Response) bool {
	if len(r.Calls) > 0 {
		return true
	}
	text := strings.TrimSpace(r.Text)
	return text != "" && !strings.Contains(text, "<function=") && !strings.Contains(text, "<tool_call>") && !strings.Contains(text, "</tool_call>")
}
