package state

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

// ResultID identifies the immutable execution task holding the query arguments
// and returned data. Reference identifies its module-owned observation, when
// available; later refreshes do not rewrite the original reference.
type CollectedEvidence struct {
	ResultID  string             `json:"result_id"`
	Reference *EvidenceReference `json:"reference,omitempty"`
}

// PreserveCollectedEvidence keeps host-owned acquisition history out of actor
// mutation schemas. Omission retains it; supplied changes are rejected.
func (s *Store) PreserveCollectedEvidence(plan Plan) (Plan, error) {
	active, _ := s.Active()
	old := map[string][]CollectedEvidence{}
	if active.Plan != nil {
		for _, step := range active.Plan.Steps {
			old[step.ID] = step.CollectedEvidence
		}
	}
	plan.Steps = append([]Step(nil), plan.Steps...)
	for i, step := range plan.Steps {
		if len(step.CollectedEvidence) > 0 && !reflect.DeepEqual(step.CollectedEvidence, old[step.ID]) {
			return plan, fmt.Errorf("step %q collected_evidence is host-owned; retain it unchanged or omit it", step.ID)
		}
		plan.Steps[i].CollectedEvidence = old[step.ID]
	}
	return plan, nil
}

// RecordCollectedEvidence records acquisition after successful admission,
// independently of semantic reconciliation. It never infers completion.
func (s *Store) RecordCollectedEvidence(ctx context.Context, resultID string) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	tasks := s.Snapshot().Tasks
	work, ok := tasks.Records[resultID]
	if !ok || work.AgentWork == nil || work.Status != "completed" || work.Error != "" {
		return nil
	}
	parent := tasks.Records[work.ParentID]
	if parent.Plan == nil || parent.Attempts == nil {
		return nil
	}
	belongs := false
	for _, id := range parent.Attempts.ResultIDs {
		belongs = belongs || id == resultID
	}
	if !belongs {
		return nil
	}
	entry := CollectedEvidence{ResultID: resultID}
	for name, partition := range s.snapshot.Knowledge {
		var value struct {
			Sources map[string]map[string]struct {
				Correlation Correlation     `json:"correlation"`
				Result      json.RawMessage `json:"result"`
			} `json:"sources"`
		}
		if json.Unmarshal(partition.Value, &value) != nil {
			continue
		}
		observation, exists := value.Sources[work.AgentWork.Server][work.AgentWork.Tool]
		if exists && observation.Correlation.RequestID == work.AgentWork.CallID && observation.Correlation.OperationID == work.AgentWork.OperationID && observation.Correlation.Turn == work.AgentWork.Turn && len(observation.Result) > 0 {
			entry.Reference = &EvidenceReference{Partition: name, Version: partition.Metadata.Version, Path: "/sources/" + pointerPart(work.AgentWork.Server) + "/" + pointerPart(work.AgentWork.Tool) + "/result"}
			break
		}
	}
	for i, step := range parent.Plan.Steps {
		if step.ID != parent.Attempts.StepID || !StepUnresolved(step) {
			continue
		}
		for _, existing := range step.CollectedEvidence {
			if existing.ResultID == resultID {
				return nil
			}
		}
		step.CollectedEvidence = append(step.CollectedEvidence, entry)
		if step.Status == "pending" {
			step.Status = "active"
		}
		parent.Plan.Steps[i] = step
		parent.Plan.Revision++
		tasks.Records[parent.ID] = parent
		if err := s.commitTasks(ctx, tasks, "step_evidence_collected", parent.ID); err != nil {
			return err
		}
		return nil
	}
	return nil
}
