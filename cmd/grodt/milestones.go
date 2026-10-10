package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode"

	"github.com/vectorphresh/0001-grodt/internal/mcp"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
)

// milestoneReporter reads committed journal entries, never model proposals.
// Each run owns one reporter and serializes access with the state store.
type milestoneReporter struct {
	store  *runstate.Store
	status *terminalStatus
	cursor uint64
	plans  map[string]runstate.Plan
	err    error
}

func milestoneText(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 160 {
		value = string(runes[:160]) + "…"
	}
	return value
}

func (r *milestoneReporter) text(value string) string {
	// Redact before normalization/truncation can split a credential.
	if r.status != nil && r.status.key != "" {
		encoded, _ := json.Marshal(r.status.key)
		value = strings.ReplaceAll(value, string(encoded[1:len(encoded)-1]), "[redacted]")
		value = strings.ReplaceAll(value, r.status.key, "[redacted]")
	}
	return milestoneText(value)
}

func (r *milestoneReporter) drain() (err error) {
	if r == nil {
		return nil
	}
	if r.err != nil {
		return r.err
	}
	defer func() {
		if err != nil {
			r.err = err
		}
	}()
	entries, cursor := r.store.JournalSince(r.cursor, "task_plan_revised", "runtime_failure", "tool_recovery_requested", "planning_stage")
	if r.plans == nil {
		r.plans = map[string]runstate.Plan{}
	}
	for _, entry := range entries {
		var lines []string
		switch entry.Kind {
		case "planning_stage":
			var stage struct {
				Action string `json:"action"`
				Status string `json:"status"`
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(entry.Data, &stage); err != nil {
				return err
			}
			prefix := "Task " + r.text(entry.TaskID) + ": Planning "
			if stage.Status == "started" {
				lines = append(lines, prefix+"started: "+r.text(stage.Action)+".")
			} else {
				message := prefix + "completed: " + r.text(stage.Status) + " (" + r.text(stage.Action) + ")"
				if stage.Reason != "" {
					message += " — " + r.text(stage.Reason)
				}
				lines = append(lines, message)
			}
		case "task_plan_revised":
			var task runstate.Task
			if err := json.Unmarshal(entry.Data, &task); err != nil {
				return err
			}
			if task.Plan == nil {
				continue
			}
			previous, exists := r.plans[entry.TaskID]
			plan := *task.Plan
			comparison := plan
			comparison.Revision = previous.Revision
			if exists && reflect.DeepEqual(comparison, previous) {
				r.plans[entry.TaskID] = plan
				r.cursor = entry.Sequence
				continue
			}
			prefix := "Task " + r.text(entry.TaskID) + ": "
			lines = append(lines, fmt.Sprintf("%sPlan accepted: revision %d, %d steps, status %s.", prefix, plan.Revision, len(plan.Steps), r.text(plan.Status)))
			statuses := map[string]string{}
			for _, step := range previous.Steps {
				statuses[step.ID] = step.Status
			}
			for _, step := range plan.Steps {
				if statuses[step.ID] != step.Status {
					lines = append(lines, prefix+"Step "+r.text(step.Status)+": "+r.text(step.Description))
				}
			}
			if plan.CurrentStep != previous.CurrentStep {
				current := "cleared"
				for _, step := range plan.Steps {
					if step.ID == plan.CurrentStep {
						current = r.text(step.Description)
					}
				}
				lines = append(lines, prefix+"Current step: "+current)
			}
			if plan.UnresolvedFocus != previous.UnresolvedFocus {
				focus := r.text(plan.UnresolvedFocus)
				if focus == "" {
					focus = "cleared"
				}
				lines = append(lines, prefix+"Unresolved focus: "+focus)
			}
			if exists && plan.Status != previous.Status {
				lines = append(lines, prefix+"Plan status: "+r.text(plan.Status))
			}
			r.plans[entry.TaskID] = plan
		case "runtime_failure":
			var diagnostic struct {
				Code    string `json:"code"`
				Attempt int    `json:"attempt"`
			}
			if err := json.Unmarshal(entry.Data, &diagnostic); err != nil {
				return err
			}
			message := map[string]string{
				"plan_initialization_required":        "Plan required: objective incomplete; only planning is available.",
				"plan_renewal_required":               "Plan renewal required: objective incomplete; ordinary execution paused.",
				"exclusive_host_correction_started":   "Planning correction requested: mixed batch; no calls dispatched and plan not applied.",
				"exclusive_host_correction_rejected":  "Planning correction rejected; no calls dispatched.",
				"exclusive_host_correction_accepted":  "Planning correction accepted.",
				"exclusive_host_correction_exhausted": "Planning correction exhausted.",
				"exclusive_host_correction_unusable":  "Planning correction failed: unusable actor responses.",
				"exclusive_host_operations_ambiguous": "Planning batch rejected: multiple planning operations; no calls dispatched.",
				"planning_correction_exhausted":       "Planning correction exhausted: 32 rejections since the last acceptance.",
				"actor_plan_capture_requested":        "Actor plan capture requested: declared intent is not persisted; no calls dispatched.",
				"standalone_plan_correction_started":  "Standalone planning correction requested: retain the full rejected proposal.",
				"planning_proposal_rejected":          "Plan proposal rejected; plan not applied.",
				"reconciliation_correction_requested": "Procedural reconciliation correction requested.",
				"tool_recovery_response":              "Tool recovery actor response obtained.",
				"tool_recovery_exhausted":             "Tool recovery exhausted.",
				"tool_recovery_completed":             "Tool recovery completed: valid tool result received.",
			}[diagnostic.Code]
			if message != "" {
				if diagnostic.Attempt > 0 {
					message += fmt.Sprintf(" Attempt %d.", diagnostic.Attempt)
				}
				lines = append(lines, message)
			}
		case "tool_recovery_requested":
			var failure mcp.InvocationFailure
			if err := json.Unmarshal(entry.Data, &failure); err != nil {
				return err
			}
			message := "Tool recovery requested: " + r.text(failure.Category) + "; execution " + r.text(failure.Execution)
			// Only quantitative fields emitted by the host failure boundary.
			for _, key := range []string{"observed_bytes", "observed_bytes_at_least", "limit_bytes", "wire_blocks", "decoded_blocks"} {
				if value, ok := failure.Details[key]; ok {
					message += fmt.Sprintf("; %s=%d", key, value)
				}
			}
			keywords := append([]string(nil), failure.ValidationKeywords...)
			sort.Strings(keywords)
			if len(keywords) > 16 {
				keywords = keywords[:16]
			}
			if len(keywords) > 0 {
				message += "; validation=" + r.text(strings.Join(keywords, ","))
			}
			lines = append(lines, message)
		}
		for _, line := range lines {
			if err := r.status.log("%s", line); err != nil {
				return err
			}
		}
		r.cursor = entry.Sequence
	}
	r.cursor = cursor
	return nil
}
