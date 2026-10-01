// Package state owns sequential, in-memory run state and its append-only journal.
// Domain knowledge remains opaque JSON. Callers must serialize store operations.
package state

import (
	"context"
	"encoding/json"
	"time"
)

const ABI = "grodt.state/v1"
const MaxValueBytes = 4 << 20
const MaxInputBytes = 8 << 20
const MaxPatchOperations = 256

// Host work has its own run budget, independent of agent cycles and structured retries.
const MaxRequestsPerResult = 8
const MaxHostExecutions = 32

type Intrinsic struct {
	RunID           string    `json:"run_id"`
	StartedAt       time.Time `json:"started_at"`
	HostExecutions  uint64    `json:"host_executions"`
	GlobalCycle     uint64    `json:"global_cycle"`
	EventSequence   uint64    `json:"event_sequence"`
	JournalSequence uint64    `json:"journal_sequence"`
}
type Snapshot struct {
	Intrinsic Intrinsic            `json:"intrinsic"`
	Tasks     TaskState            `json:"tasks"`
	Knowledge map[string]Partition `json:"knowledge"`
}
type TaskState struct {
	Records map[string]Task `json:"records"`
	Stack   []string        `json:"stack"`
}
type Task struct {
	Order      uint64     `json:"order"`
	Work       *HostWork  `json:"work,omitempty"`
	ID         string     `json:"id"`
	ParentID   string     `json:"parent_id,omitempty"`
	Objective  string     `json:"objective"`
	Input      string     `json:"input"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Cycles     uint64     `json:"cycles"`
	Result     string     `json:"result,omitempty"`
	Error      string     `json:"error,omitempty"`
}
type Partition struct {
	Metadata Metadata        `json:"metadata"`
	Value    json.RawMessage `json:"value"`
}
type Metadata struct {
	Version               uint64     `json:"version"`
	LastProcessedSequence uint64     `json:"last_processed_sequence"`
	LastUpdateSequence    uint64     `json:"last_update_sequence"`
	LastUpdateAt          *time.Time `json:"last_update_at,omitempty"`
	Stale                 bool       `json:"stale"`
	ConsecutiveFailures   uint64     `json:"consecutive_failures"`
	LastError             *Failure   `json:"last_error,omitempty"`
}
type Failure struct {
	Kind    string `json:"kind"`
	Code    string `json:"code"`
	EventID string `json:"event_id,omitempty"`
}
type Source struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type HostRequest struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}
type HostWork struct {
	Partition string      `json:"partition"`
	EventID   string      `json:"event_id"`
	Request   HostRequest `json:"request"`
}
type Correlation struct {
	Partition string `json:"partition"`
	RequestID string `json:"request_id"`
}
type Event struct {
	TaskID      string          `json:"task_id,omitempty"`
	Correlation *Correlation    `json:"correlation,omitempty"`
	ID          string          `json:"id"`
	Sequence    uint64          `json:"sequence"`
	Time        time.Time       `json:"timestamp"`
	Source      Source          `json:"source"`
	Payload     json.RawMessage `json:"payload"`
}
type Entry struct {
	Sequence  uint64          `json:"sequence"`
	Time      time.Time       `json:"timestamp"`
	Kind      string          `json:"kind"`
	EventID   string          `json:"event_id,omitempty"`
	TaskID    string          `json:"task_id,omitempty"`
	Partition string          `json:"partition,omitempty"`
	Data      json.RawMessage `json:"data"`
}

// Processor is implemented by the WASM adapter. Process returns an encoded v1
// result; implementations never receive pointers into authoritative state.
type Processor interface {
	Process(context.Context, json.RawMessage, Event) (json.RawMessage, error)
	Close(context.Context) error
}
type Definition struct {
	Name    string
	Schema  json.RawMessage
	Initial json.RawMessage
	Module  Processor
}

// RuntimeError carries only a host-selected code, never guest text or traces.
type RuntimeError struct{ Code string }

func (e *RuntimeError) Error() string { return "state module execution failed: " + e.Code }
