package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

var ErrHostBudget = errors.New("state host execution budget exhausted")

func sameRequest(a, b HostRequest) bool {
	var x, y any
	da, db := json.NewDecoder(bytes.NewReader(a.Payload)), json.NewDecoder(bytes.NewReader(b.Payload))
	da.UseNumber()
	db.UseNumber()
	return a.Kind == b.Kind && da.Decode(&x) == nil && db.Decode(&y) == nil && reflect.DeepEqual(x, y)
}
func (s *Store) existingRequest(partition, id string) (Task, bool) {
	for _, t := range s.snapshot.Tasks.Records {
		if t.Work != nil && t.Work.Partition == partition && t.Work.Request.ID == id {
			return t, true
		}
	}
	return Task{}, false
}
func (s *Store) checkRequests(partition string, e Event, requests []HostRequest) error {
	for _, r := range requests {
		if e.TaskID == "" {
			return errors.New("host request requires a causal task")
		}
		if t, exists := s.existingRequest(partition, r.ID); exists && !sameRequest(t.Work.Request, r) {
			return errors.New("conflicting request identity")
		}
	}
	return nil
}
func (s *Store) queueRequests(ctx context.Context, partition string, e Event, requests []HostRequest) error {
	for _, r := range requests {
		if t, exists := s.existingRequest(partition, r.ID); exists {
			s.appendEntry(Entry{Kind: "host_request_duplicate", TaskID: t.ID, EventID: e.ID, Partition: partition, Data: encode(r)})
			continue
		}
		tasks := s.Snapshot().Tasks
		s.taskSequence++
		id := fmt.Sprintf("%s/task/%d", s.snapshot.Intrinsic.RunID, s.taskSequence)
		tasks.Records[id] = Task{ID: id, Order: s.taskSequence, ParentID: e.TaskID, Objective: "Execute HTTP host request", Input: string(r.Payload), Status: "pending", CreatedAt: s.now().UTC(), Work: &HostWork{Partition: partition, EventID: e.ID, Request: r}}
		if err := s.commitTasks(ctx, tasks, "task_created", id); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) pendingChild(parent string) (Task, bool) {
	var next Task
	for _, t := range s.snapshot.Tasks.Records {
		if t.ParentID == parent && t.Status == "pending" && (next.ID == "" || t.Order < next.Order) {
			next = t
		}
	}
	return next, next.ID != ""
}
func (s *Store) activate(ctx context.Context, id string) error {
	tasks := s.Snapshot().Tasks
	t := tasks.Records[id]
	if len(tasks.Stack) == 0 || t.Status != "pending" || tasks.Stack[len(tasks.Stack)-1] != t.ParentID {
		return errors.New("invalid task activation")
	}
	p := tasks.Records[t.ParentID]
	p.Status = "waiting"
	tasks.Records[p.ID] = p
	now := s.now().UTC()
	t.StartedAt = &now
	t.Status = "running"
	tasks.Records[id] = t
	tasks.Stack = append(tasks.Stack, id)
	return s.commitTasks(ctx, tasks, "task_started", id)
}

// drainHostWork runs only after an entire event broadcast. Descendants finish
// before their parent resumes; sibling requests retain creation order. Tasks are
// the work queue and retain outcomes, so an executed task is never re-executed.
func (s *Store) drainHostWork(ctx context.Context) error {
	for {
		if err := s.check(ctx); err != nil {
			return err
		}
		if s.hostExhausted {
			return ErrHostBudget
		}
		active, ok := s.Active()
		if !ok {
			return nil
		}
		if child, ok := s.pendingChild(active.ID); ok {
			if err := s.activate(ctx, child.ID); err != nil {
				return err
			}
			continue
		}
		if active.Work == nil {
			return nil
		}
		if active.Result != "" {
			status := "completed"
			if active.Error != "" {
				status = "failed"
			}
			if err := s.finish(ctx, status, active.Result, active.Error); err != nil {
				return err
			}
			continue
		}
		if s.snapshot.Intrinsic.HostExecutions >= MaxHostExecutions {
			s.hostExhausted = true
			s.appendEntry(Entry{Kind: "runtime_failure", TaskID: active.ID, Data: encode(map[string]string{"code": "host_execution_budget_exhausted"})})
			if err := s.End(context.WithoutCancel(ctx), "failed"); err != nil {
				return err
			}
			return ErrHostBudget
		}
		s.snapshot.Intrinsic.HostExecutions++
		s.appendEntry(Entry{Kind: "host_execution_started", TaskID: active.ID, Data: encode(active.Work)})
		outcome := s.executeHTTP(ctx, active.Work.Request.Payload)
		tasks := s.Snapshot().Tasks
		t := tasks.Records[active.ID]
		t.Result = string(encode(outcome))
		t.Error = outcome.Error
		if t.Error == "" && outcome.StatusCode >= 400 {
			t.Error = "http_status"
		}
		tasks.Records[t.ID] = t
		// Preserve the once-only outcome even when cancellation prevents broadcast.
		if err := s.commitTasks(context.WithoutCancel(ctx), tasks, "host_execution_finished", t.ID); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		correlation := &Correlation{Partition: t.Work.Partition, RequestID: t.Work.Request.ID}
		if _, err := s.admit(ctx, Source{Kind: "http", ID: t.ID}, json.RawMessage(t.Result), correlation); err != nil {
			return err
		}
	}
}
