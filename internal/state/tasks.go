package state

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

//go:embed tasks.schema.json
var taskSchema json.RawMessage

func (s *Store) validateTasks(ctx context.Context, tasks TaskState) error {
	r, err := s.validator.Validate(ctx, taskSchema, encode(tasks))
	if err != nil {
		return err
	}
	if !r.Valid {
		return errors.New("invalid task state")
	}
	return nil
}
func (s *Store) commitTasks(ctx context.Context, tasks TaskState, kind, id string) error {
	if err := s.validateTasks(ctx, tasks); err != nil {
		return err
	}
	s.snapshot.Tasks = tasks
	s.appendEntry(Entry{Kind: kind, TaskID: id, Data: encode(tasks.Records[id])})
	return nil
}

// Push is an explicit host operation, not a model-planning policy. The current
// top becomes the parent; completed siblings remain in Records.
func (s *Store) Push(ctx context.Context, objective, input string) (string, error) {
	if err := s.check(ctx); err != nil {
		return "", err
	}
	if strings.TrimSpace(objective) == "" {
		return "", errors.New("task objective is required")
	}
	tasks := s.Snapshot().Tasks
	if len(tasks.Stack) == 0 && len(tasks.Records) != 0 {
		return "", errors.New("root task already exists")
	}
	parent := ""
	if len(tasks.Stack) > 0 {
		parent = tasks.Stack[len(tasks.Stack)-1]
		p := tasks.Records[parent]
		p.Status = "waiting"
		tasks.Records[parent] = p
	}
	id := fmt.Sprintf("%s/task/%d", s.snapshot.Intrinsic.RunID, s.taskSequence+1)
	now := s.now().UTC()
	tasks.Records[id] = Task{ID: id, ParentID: parent, Objective: objective, Input: input, Status: "running", CreatedAt: now, StartedAt: &now, Order: s.taskSequence + 1}
	tasks.Stack = append(tasks.Stack, id)
	if err := s.commitTasks(ctx, tasks, "task_created", id); err != nil {
		return "", err
	}
	s.taskSequence++
	return id, nil
}
func (s *Store) Active() (Task, bool) {
	tasks := s.Snapshot().Tasks
	if len(tasks.Stack) == 0 {
		return Task{}, false
	}
	return tasks.Records[tasks.Stack[len(tasks.Stack)-1]], true
}
func (s *Store) BeginCycle(ctx context.Context) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	tasks := s.Snapshot().Tasks
	if len(tasks.Stack) == 0 {
		return errors.New("no active task")
	}
	id := tasks.Stack[len(tasks.Stack)-1]
	t := tasks.Records[id]
	t.Cycles++
	tasks.Records[id] = t
	if err := s.validateTasks(ctx, tasks); err != nil {
		return err
	}
	s.snapshot.Tasks = tasks
	s.snapshot.Intrinsic.GlobalCycle++
	s.appendEntry(Entry{Kind: "cycle_started", TaskID: id, Data: encode(map[string]uint64{"global_cycle": s.snapshot.Intrinsic.GlobalCycle, "task_cycle": t.Cycles})})
	return nil
}
func (s *Store) Complete(ctx context.Context, result string) error {
	return s.finish(ctx, "completed", result, "")
}
func (s *Store) finish(ctx context.Context, status, result, code string) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	tasks := s.Snapshot().Tasks
	if len(tasks.Stack) == 0 {
		return errors.New("no active task")
	}
	id := tasks.Stack[len(tasks.Stack)-1]
	t := tasks.Records[id]
	now := s.now().UTC()
	if _, ok := s.pendingChild(id); ok {
		return errors.New("task has pending work")
	}
	t.Status = status
	t.Error = code
	t.FinishedAt = &now
	t.Result = result
	tasks.Records[id] = t
	tasks.Stack = tasks.Stack[:len(tasks.Stack)-1]
	if len(tasks.Stack) > 0 {
		parent := tasks.Stack[len(tasks.Stack)-1]
		p := tasks.Records[parent]
		p.Status = "running"
		tasks.Records[parent] = p
	}
	return s.commitTasks(ctx, tasks, "task_"+status, id)
}

// End marks the active lineage and pending work terminal without inventing completion.
// It is also used with a non-cancelled cleanup context after run cancellation.
func (s *Store) End(ctx context.Context, status string) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	if status != "failed" && status != "cancelled" && status != "incomplete" {
		return errors.New("invalid terminal task status")
	}
	tasks := s.Snapshot().Tasks
	now := s.now().UTC()
	ids := []string{}
	for id, task := range tasks.Records {
		if task.Status == "pending" || task.Status == "running" || task.Status == "waiting" {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return tasks.Records[ids[i]].Order < tasks.Records[ids[j]].Order })
	for _, id := range ids {
		t := tasks.Records[id]
		t.Status = status
		t.FinishedAt = &now
		t.Error = status
		tasks.Records[id] = t
	}
	tasks.Stack = []string{}
	if err := s.validateTasks(ctx, tasks); err != nil {
		return err
	}
	s.snapshot.Tasks = tasks
	for _, id := range ids {
		s.appendEntry(Entry{Kind: "task_" + status, TaskID: id, Data: encode(tasks.Records[id])})
	}
	return nil
}
