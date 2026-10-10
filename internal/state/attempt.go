package state

// Attempts belong to the reasoning task so plan edits cannot erase the streak.
// CurrentStep is dispatch scope, not a claim that every result advances it.
const ReplanAfterNoProgress = 3

type AttemptSummary struct {
	Total                 uint64   `json:"total"`
	ConsecutiveNoProgress uint64   `json:"consecutive_no_progress"`
	StepID                string   `json:"step_id"`
	PlanRevision          uint64   `json:"plan_revision"`
	InformationGaps       []string `json:"information_gaps"`
	ResultIDs             []string `json:"result_ids"`
	Outcome               string   `json:"outcome"`
	Reason                string   `json:"reason"`
	ReplanRequired        bool     `json:"replan_required"`
}

func beginAttempt(task *Task, ids []string) {
	next := AttemptSummary{StepID: task.Plan.CurrentStep, PlanRevision: task.Plan.Revision, ResultIDs: append([]string(nil), ids...), InformationGaps: []string{}, Outcome: "in_progress"}
	if task.Attempts != nil {
		next.Total = task.Attempts.Total
		next.ConsecutiveNoProgress = task.Attempts.ConsecutiveNoProgress
	}
	next.Total++
	next.ReplanRequired = next.ConsecutiveNoProgress >= ReplanAfterNoProgress
	for _, step := range task.Plan.Steps {
		if step.ID == next.StepID {
			next.InformationGaps = append(next.InformationGaps, step.InformationGaps...)
		}
	}
	task.Attempts = &next
}

// FinishAttempt records only an accepted reconciliation, once per batch.
// Rejected candidates and correction inferences never change these counters.
func (s *Store) FinishAttempt(taskID string, progress bool, reason string) {
	task, ok := s.snapshot.Tasks.Records[taskID]
	if !ok || task.Attempts == nil || task.Attempts.Outcome != "in_progress" {
		return
	}
	next := *task.Attempts
	next.Reason = reason
	next.Outcome = "no_progress"
	next.ConsecutiveNoProgress++
	if progress {
		next.Outcome = "progress"
		next.ConsecutiveNoProgress = 0
	}
	next.ReplanRequired = next.ConsecutiveNoProgress >= ReplanAfterNoProgress
	task.Attempts = &next
	s.snapshot.Tasks.Records[taskID] = task
	s.appendEntry(Entry{Kind: "step_attempt_reconciled", TaskID: taskID, Data: encode(next)})
}
