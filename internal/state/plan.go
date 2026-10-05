package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
)

// Plans describe public intent and outcomes, never private reasoning. Revision
// bodies live in the journal; only the latest bounded plan is projected.
type Plan struct {
	Revision           uint64              `json:"revision"`
	Description        string              `json:"description"`
	CompletionCriteria string              `json:"completion_criteria,omitempty"`
	Status             string              `json:"status"`
	Outcome            string              `json:"outcome,omitempty"`
	Evidence           []EvidenceReference `json:"evidence_refs,omitempty"`
	InformationGaps    []string            `json:"information_gaps,omitempty"`
	Steps              []Step              `json:"steps"`
	CurrentStep        string              `json:"current_step,omitempty"`
}
type Step struct {
	ID                 string              `json:"id"`
	Description        string              `json:"description"`
	CompletionCriteria string              `json:"completion_criteria,omitempty"`
	Status             string              `json:"status"`
	Outcome            string              `json:"outcome,omitempty"`
	Evidence           []EvidenceReference `json:"evidence_refs,omitempty"`
	InformationGaps    []string            `json:"information_gaps,omitempty"`
}
type EvidenceReference struct {
	Partition string `json:"partition"`
	Version   uint64 `json:"version"`
	Path      string `json:"path"`
}

func boundedText(v string, required bool) bool {
	return len(v) <= 512 && (!required || strings.TrimSpace(v) != "")
}
func validPlanStatus(v string) bool {
	switch v {
	case "pending", "active", "partial", "satisfied", "invalidated", "failed":
		return true
	}
	return false
}
func validGaps(gaps []string) bool {
	if len(gaps) > 8 {
		return false
	}
	for _, g := range gaps {
		if !boundedText(g, true) {
			return false
		}
	}
	return true
}

// ResolveEvidence accepts only actual current module-owned state. It never
// treats an actor claim or a raw tool invocation as authoritative evidence.
func (s *Store) ResolveEvidence(ref EvidenceReference) (json.RawMessage, error) {
	p, ok := s.snapshot.Knowledge[ref.Partition]
	if !ok || p.Metadata.Stale || p.Metadata.Version != ref.Version {
		return nil, errors.New("evidence partition missing, stale, or version changed")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(p.Value))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if ref.Path != "" {
		if !strings.HasPrefix(ref.Path, "/") || len(ref.Path) > 256 {
			return nil, errors.New("invalid evidence pointer")
		}
		for _, part := range strings.Split(ref.Path[1:], "/") {
			for i := 0; i < len(part); i++ {
				if part[i] == '~' && (i+1 >= len(part) || (part[i+1] != '0' && part[i+1] != '1')) {
					return nil, errors.New("invalid evidence pointer escape")
				}
			}
			part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			switch node := value.(type) {
			case map[string]any:
				value, ok = node[part]
				if !ok {
					return nil, errors.New("evidence path missing")
				}
			case []any:
				index, err := strconv.Atoi(part)
				if err != nil || index < 0 || index >= len(node) || strconv.Itoa(index) != part {
					return nil, errors.New("invalid evidence array index")
				}
				value = node[index]
			default:
				return nil, errors.New("evidence path missing")
			}
		}
	}
	return encode(value), nil
}

// CheckPlan validates a proposal without mutating state. Accepted satisfied
// steps cannot be rewritten or removed; explicit invalidation preserves their
// description, criteria, outcome and original provenance.
func (s *Store) CheckPlan(plan Plan) error {
	t, ok := s.Active()
	if !ok || t.AgentWork != nil || t.Work != nil {
		return errors.New("no active reasoning task")
	}
	if !boundedText(plan.Description, true) || !boundedText(plan.CompletionCriteria, false) || !boundedText(plan.Outcome, false) || !validPlanStatus(plan.Status) || !validGaps(plan.InformationGaps) || len(plan.Steps) > 32 || len(plan.Evidence) > 8 {
		return errors.New("invalid or oversized plan")
	}
	expected := uint64(0)
	if t.Plan != nil {
		expected = t.Plan.Revision
	}
	if plan.Revision != expected {
		return errors.New("plan revision conflict")
	}
	if t.Plan != nil && (t.Plan.Status == "satisfied" || t.Plan.Status == "invalidated") {
		comparison := plan
		comparison.Status = t.Plan.Status
		if !reflect.DeepEqual(comparison, *t.Plan) || (plan.Status != t.Plan.Status && plan.Status != "invalidated") {
			return errors.New("completed plan is immutable; invalidate or create a new intent")
		}
	}
	old := map[string]Step{}
	if t.Plan != nil {
		for _, step := range t.Plan.Steps {
			old[step.ID] = step
		}
	}
	seen := map[string]bool{}
	validateEvidence := func(refs []EvidenceReference) error {
		for _, ref := range refs {
			if _, err := s.ResolveEvidence(ref); err != nil {
				return err
			}
		}
		return nil
	}
	if plan.Status == "satisfied" && (plan.Outcome == "" || len(plan.Evidence) == 0 || len(plan.InformationGaps) > 0) {
		return errors.New("satisfaction requires outcome, evidence, and no gaps")
	}
	if err := validateEvidence(plan.Evidence); err != nil {
		return err
	}
	for _, step := range plan.Steps {
		if !namePattern.MatchString(step.ID) || seen[step.ID] || !boundedText(step.Description, true) || !boundedText(step.CompletionCriteria, false) || !boundedText(step.Outcome, false) || !validPlanStatus(step.Status) || !validGaps(step.InformationGaps) || len(step.Evidence) > 8 {
			return errors.New("invalid or duplicate step")
		}
		seen[step.ID] = true
		if prev, exists := old[step.ID]; exists && (prev.Status == "satisfied" || prev.Status == "invalidated") {
			comparison := step
			comparison.Status = prev.Status
			if !reflect.DeepEqual(comparison, prev) || (step.Status != prev.Status && step.Status != "invalidated") {
				return errors.New("completed step history is immutable; invalidate explicitly or add a new step")
			}
			continue
		}
		if step.Status == "satisfied" && (step.Outcome == "" || len(step.Evidence) == 0 || len(step.InformationGaps) > 0) {
			return errors.New("step satisfaction requires outcome, evidence, and no gaps")
		}
		if err := validateEvidence(step.Evidence); err != nil {
			return err
		}
	}
	for id, step := range old {
		if (step.Status == "satisfied" || step.Status == "invalidated") && !seen[id] {
			return errors.New("cannot remove completed step history")
		}
	}
	if plan.CurrentStep != "" {
		found := false
		for _, step := range plan.Steps {
			if step.ID == plan.CurrentStep {
				found = true
				if step.Status == "satisfied" || step.Status == "failed" {
					return errors.New("current step must be unresolved")
				}
			}
		}
		if !found {
			return errors.New("current step missing")
		}
	}
	if plan.Status == "satisfied" {
		for _, step := range plan.Steps {
			if step.Status != "satisfied" {
				return errors.New("plan contains unresolved steps")
			}
		}
	}
	return nil
}

// RevisePlan is a host acceptance operation. The actor-facing boundary must
// evaluate new satisfaction claims before calling it.
func (s *Store) RevisePlan(ctx context.Context, plan Plan) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	if err := s.CheckPlan(plan); err != nil {
		return err
	}
	tasks := s.Snapshot().Tasks
	id := tasks.Stack[len(tasks.Stack)-1]
	t := tasks.Records[id]
	plan.Revision++
	var owned Plan
	if err := json.Unmarshal(encode(plan), &owned); err != nil {
		return err
	}
	t.Plan = &owned
	tasks.Records[id] = t
	return s.commitTasks(ctx, tasks, "task_plan_revised", id)
}

func (s *Store) AbandonChild(ctx context.Context) error {
	t, ok := s.Active()
	if !ok || t.ParentID == "" || t.AgentWork != nil || t.Work != nil {
		return errors.New("only a reasoning child task can be abandoned")
	}
	return s.finish(ctx, "incomplete", "", "abandoned")
}
func (s *Store) RecordPlanEvaluation(proposal, decision json.RawMessage) {
	t, _ := s.Active()
	s.appendEntry(Entry{Kind: "task_plan_evaluated", TaskID: t.ID, Data: encode(map[string]json.RawMessage{"proposal": proposal, "decision": decision})})
}

// CompleteObjective follows an independently confirmed root objective outcome.
// Unresolved child intents end incomplete, rather than inheriting satisfaction.
func (s *Store) CompleteObjective(ctx context.Context, result string) error {
	for {
		t, ok := s.Active()
		if !ok {
			return errors.New("no active objective")
		}
		if t.ParentID == "" {
			return s.Complete(ctx, result)
		}
		if t.AgentWork != nil || t.Work != nil {
			return errors.New("objective has active execution work")
		}
		if t.Plan != nil && t.Plan.Status == "satisfied" {
			if err := s.Complete(ctx, t.Plan.Outcome); err != nil {
				return err
			}
		} else {
			if err := s.AbandonChild(ctx); err != nil {
				return err
			}
		}
	}
}

// Reconciliation audit records are not admission events and cannot mutate
// module-owned knowledge or recursively invoke procedural reconciliation.
func (s *Store) RecordReconciliation(taskID string, evidence, proposed json.RawMessage, code string) {
	if proposed == nil {
		proposed = json.RawMessage(`null`)
	}
	s.appendEntry(Entry{Kind: "procedural_reconciliation", TaskID: taskID, Data: encode(struct {
		Evidence json.RawMessage `json:"accepted_evidence"`
		Proposed json.RawMessage `json:"proposed_changes"`
		Error    string          `json:"error,omitempty"`
	}{evidence, proposed, code})})
}
