package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// QueueAgentWork records a fully preflighted batch as siblings of the active
// reasoning task. State modules have no API or result variant for this authority.
func (s *Store) QueueAgentWork(ctx context.Context, work []AgentWork) ([]string, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	active, ok := s.Active()
	if !ok {
		return nil, errors.New("agent work requires active parent")
	}
	tasks := s.Snapshot().Tasks
	ids := make([]string, len(work))
	for i, w := range work {
		order := s.taskSequence + uint64(i) + 1
		id := fmt.Sprintf("%s/task/%d", s.snapshot.Intrinsic.RunID, order)
		ids[i] = id
		copy := w
		copy.Arguments = append(json.RawMessage(nil), w.Arguments...)
		tasks.Records[id] = Task{ID: id, ParentID: active.ID, Order: order, Objective: "Invoke external tool", Input: string(w.Arguments), Status: "pending", CreatedAt: s.now().UTC(), AgentWork: &copy}
	}
	if err := s.validateTasks(ctx, tasks); err != nil {
		return nil, err
	}
	s.snapshot.Tasks = tasks
	s.taskSequence += uint64(len(work))
	for _, id := range ids {
		s.appendEntry(Entry{Kind: "task_created", TaskID: id, Data: encode(tasks.Records[id])})
	}
	return ids, nil
}
func (s *Store) ActivateAgentWork(ctx context.Context, id string) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	t, ok := s.snapshot.Tasks.Records[id]
	if !ok || t.AgentWork == nil {
		return errors.New("not agent work")
	}
	next, ok := s.pendingChild(t.ParentID)
	if !ok || next.ID != id {
		return errors.New("agent work out of order")
	}
	return s.activate(ctx, id)
}

// RecordAgentOutcome retains the completed external invocation before admission,
// even if cancellation or a later module HTTP failure prevents continuation.
func (s *Store) RecordAgentOutcome(ctx context.Context, result json.RawMessage, code string) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	t, ok := s.Active()
	if !ok || t.AgentWork == nil || t.Result != "" {
		return errors.New("invalid agent outcome")
	}
	if !json.Valid(result) {
		return errors.New("invalid agent result")
	}
	tasks := s.Snapshot().Tasks
	t.Result = string(result)
	t.Error = code
	tasks.Records[t.ID] = t
	return s.commitTasks(ctx, tasks, "agent_execution_finished", t.ID)
}
func (s *Store) AdmitAgentOutcome(ctx context.Context, payload json.RawMessage) (Event, error) {
	t, ok := s.Active()
	if !ok || t.AgentWork == nil || t.Result == "" {
		return Event{}, errors.New("missing agent outcome")
	}
	w := t.AgentWork
	e, err := s.admit(ctx, Source{Kind: "mcp", ID: t.ID}, payload, &Correlation{OperationID: w.OperationID, Turn: w.Turn, RequestID: w.CallID})
	if err == nil {
		err = s.drainHostWork(ctx)
	}
	return e, err
}
func (s *Store) FinishAgentWork(ctx context.Context) error {
	t, ok := s.Active()
	if !ok || t.AgentWork == nil || t.Result == "" {
		return errors.New("missing agent outcome")
	}
	status := "completed"
	if t.Error != "" {
		status = "failed"
	}
	return s.finish(ctx, status, t.Result, t.Error)
}
