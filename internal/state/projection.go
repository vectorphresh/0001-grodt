package state

import (
	"encoding/json"
	"sort"
)

// ModelJSON projects current knowledge and actionable tasks. JSON and Journal
// remain the full audit interfaces; completed work is not model context.
// Partition values are module-owned current world state, not event archives.
func (s *Store) ModelJSON() json.RawMessage {
	type taskView struct {
		Attempts         *AttemptSummary   `json:"attempts,omitempty"`
		Plan             *Plan             `json:"plan,omitempty"`
		EvidenceState    map[string]string `json:"evidence_state,omitempty"`
		EstablishedSteps []string          `json:"established_steps,omitempty"`
		UnresolvedSteps  []string          `json:"unresolved_steps,omitempty"`
		CurrentPosition  int               `json:"current_position,omitempty"`
		PlanGuidance     string            `json:"plan_guidance,omitempty"`
		ID               string            `json:"id"`
		ParentID         string            `json:"parent_id,omitempty"`
		Objective        string            `json:"objective"`
		Status           string            `json:"status"`
		Cycles           uint64            `json:"cycles"`
		Server           string            `json:"server,omitempty"`
		Tool             string            `json:"tool,omitempty"`
		Partition        string            `json:"partition,omitempty"`
		WorkKind         string            `json:"work_kind,omitempty"`
	}
	view := struct {
		Intrinsic Intrinsic `json:"intrinsic"`
		Tasks     struct {
			Records map[string]taskView `json:"records"`
			Stack   []string            `json:"stack"`
		} `json:"tasks"`
		Knowledge           map[string]Partition           `json:"knowledge"`
		EstablishedProgress map[string][]ProjectedProgress `json:"established_progress"`
		ActiveFocus         map[string][]ProjectedProgress `json:"active_focus"`
	}{Intrinsic: s.snapshot.Intrinsic, Knowledge: map[string]Partition{}, EstablishedProgress: map[string][]ProjectedProgress{}, ActiveFocus: map[string][]ProjectedProgress{}}
	view.Tasks.Records = map[string]taskView{}
	view.Tasks.Stack = s.snapshot.Tasks.Stack
	relevant := s.actionableTasks()
	for id, t := range s.snapshot.Tasks.Records {
		if relevant[id] {
			v := taskView{ID: t.ID, ParentID: t.ParentID, Objective: t.Objective, Status: t.Status, Cycles: t.Cycles, Plan: t.Plan}
			v.Attempts = t.Attempts
			if t.Plan != nil {
				projected := *t.Plan
				projected.AdvanceCurrentStep()
				v.Plan = &projected
				v.PlanGuidance = "Your accepted plan persists until an accepted update changes it. Follow current_step; unmentioned steps remain in their accepted order. Update your plan before changing its procedure; tactical choices within the current step need no update."
				for i, step := range projected.Steps {
					if step.ID == projected.CurrentStep {
						v.CurrentPosition = i + 1
					}
				}
				v.EvidenceState = map[string]string{}
				refs := append([]EvidenceReference(nil), t.Plan.Evidence...)
				for _, step := range t.Plan.Steps {
					refs = append(refs, step.Evidence...)
					if step.Status == "satisfied" {
						v.EstablishedSteps = append(v.EstablishedSteps, step.ID)
					} else if step.Status != "failed" && step.Status != "invalidated" {
						v.UnresolvedSteps = append(v.UnresolvedSteps, step.ID)
					}
				}
				for _, ref := range refs {
					v.EvidenceState[string(encode(ref))] = s.EvidenceFreshness(ref)
				}
			}
			if t.AgentWork != nil {
				v.Server, v.Tool = t.AgentWork.Server, t.AgentWork.Tool
			}
			if t.Work != nil {
				v.Partition, v.WorkKind = t.Work.Partition, t.Work.Request.Kind
			}
			view.Tasks.Records[id] = v
		}
	}
	for name, p := range s.snapshot.Knowledge {
		view.Knowledge[name] = Partition{Metadata: p.Metadata, Value: p.Value}
	}
	for _, record := range s.ActionableProgress() {
		if record.Kind == "focus" {
			view.ActiveFocus[record.Partition] = append(view.ActiveFocus[record.Partition], record)
		} else {
			view.EstablishedProgress[record.Partition] = append(view.EstablishedProgress[record.Partition], record)
		}
	}
	return encode(view)
}

// ProjectedProgress keeps module-declared status separate from observation freshness.
type ProjectedProgress struct {
	ProgressRecord
	Partition string `json:"partition"`
	Freshness string `json:"freshness"`
}

func (s *Store) actionableTasks() map[string]bool {
	lineage := map[string]bool{}
	for _, id := range s.snapshot.Tasks.Stack {
		lineage[id] = true
	}
	ids := map[string]bool{}
	for id, t := range s.snapshot.Tasks.Records {
		if t.Status == "running" || t.Status == "pending" || t.Status == "waiting" || (t.Plan != nil && lineage[t.ParentID]) {
			ids[id] = true
		}
	}
	return ids
}

// ActionableProgress is shared by actor and bounded reconciliation projections.
func (s *Store) ActionableProgress() []ProjectedProgress {
	out := []ProjectedProgress{}
	relevant := s.actionableTasks()
	names := []string{}
	for name := range s.snapshot.Knowledge {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := s.snapshot.Knowledge[name]
		for _, record := range p.Progress {
			if relevant[record.TaskID] {
				out = append(out, ProjectedProgress{ProgressRecord: record, Partition: name, Freshness: s.EvidenceFreshness(EvidenceReference{Partition: name, Version: record.KnowledgeVersion})})
			}
		}
	}
	return out
}
