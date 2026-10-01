package state

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type processor struct {
	fn     func(json.RawMessage, Event) (json.RawMessage, error)
	closed bool
}

func (p *processor) Process(_ context.Context, s json.RawMessage, e Event) (json.RawMessage, error) {
	return p.fn(s, e)
}
func (p *processor) Close(context.Context) error { p.closed = true; return nil }
func newStore(t *testing.T, defs ...Definition) *Store {
	t.Helper()
	s, err := New(context.Background(), defs, Options{RunID: "test-run", Now: func() time.Time { return time.Unix(100, 0).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	return s
}
func emit(t *testing.T, s *Store) Event {
	t.Helper()
	e, err := s.Admit(context.Background(), Source{Kind: "runtime", ID: "test-source"}, json.RawMessage(`{"observation":1}`))
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestPartitionOutcomesAndIsolation(t *testing.T) {
	status := `{"status":"error"}`
	a := &processor{fn: func(json.RawMessage, Event) (json.RawMessage, error) { return json.RawMessage(status), nil }}
	b := &processor{fn: func(json.RawMessage, Event) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"mutation","replace":9}`), nil
	}}
	s := newStore(t, Definition{"a", json.RawMessage(`{"type":"integer"}`), json.RawMessage(`1`), a}, Definition{"b", json.RawMessage(`{"type":"integer"}`), json.RawMessage(`2`), b})
	emit(t, s)
	snap := s.Snapshot()
	if !snap.Knowledge["a"].Metadata.Stale || string(snap.Knowledge["a"].Value) != "1" || string(snap.Knowledge["b"].Value) != "9" {
		t.Fatal("partition failure isolation")
	}
	status = `{"status":"ignored"}`
	emit(t, s)
	p := s.Snapshot().Knowledge["a"]
	if !p.Metadata.Stale || p.Metadata.LastProcessedSequence != 0 || p.Metadata.ConsecutiveFailures != 1 {
		t.Fatal("ignored cleared failure")
	}
	status = `{"status":"processed"}`
	e := emit(t, s)
	p = s.Snapshot().Knowledge["a"]
	if p.Metadata.Stale || p.Metadata.LastProcessedSequence != e.Sequence || p.Metadata.LastUpdateSequence != 0 || p.Metadata.Version != 0 || p.Metadata.LastError != nil || p.Metadata.ConsecutiveFailures != 0 {
		t.Fatal("processed semantics")
	}
	status = `{"status":"mutation","replace":"bad"}`
	emit(t, s)
	p = s.Snapshot().Knowledge["a"]
	if !p.Metadata.Stale || p.Metadata.LastError.Kind != "mutation_rejected" || string(p.Value) != "1" {
		t.Fatal("invalid value committed")
	}
	status = `{"status":"mutation","replace":3}`
	e = emit(t, s)
	p = s.Snapshot().Knowledge["a"]
	if p.Metadata.Stale || p.Metadata.LastProcessedSequence != e.Sequence || p.Metadata.LastUpdateSequence != e.Sequence || p.Metadata.Version != 1 || string(p.Value) != "3" {
		t.Fatal("mutation semantics")
	}
	journal := s.Journal()
	for i, entry := range journal {
		if entry.Sequence != uint64(i+1) {
			t.Fatal("journal order")
		}
	}
	if s.Snapshot().Intrinsic.EventSequence != 5 || s.Snapshot().Intrinsic.JournalSequence <= 5 {
		t.Fatal("event and commit ordering conflated")
	}
}
func TestPatchesAreAtomicAndStrict(t *testing.T) {
	const original = `{"keep":9007199254740993,"array":[1,2],"old":true}`
	for _, tt := range []struct {
		name, patch string
		valid       bool
	}{
		{"create update delete", `[{"op":"add","path":"/new","value":"x"},{"op":"replace","path":"/array/0","value":5},{"op":"remove","path":"/old"}]`, true},
		{"partial failure", `[{"op":"replace","path":"/keep","value":1},{"op":"remove","path":"/missing"}]`, false},
		{"negative index", `[{"op":"remove","path":"/array/-1"}]`, false},
		{"missing ancestor", `[{"op":"add","path":"/missing/child","value":1}]`, false},
		{"cross partition path", `[{"op":"replace","path":"/knowledge/other/value","value":2}]`, false},
		{"schema failure", `[{"op":"remove","path":"/keep"}]`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := &processor{fn: func(json.RawMessage, Event) (json.RawMessage, error) {
				return json.RawMessage(`{"status":"mutation","patch":` + tt.patch + `}`), nil
			}}
			s := newStore(t, Definition{"p", json.RawMessage(`{"type":"object","required":["keep"]}`), json.RawMessage(original), m})
			emit(t, s)
			p := s.Snapshot().Knowledge["p"]
			if p.Metadata.Stale == tt.valid {
				t.Fatal("wrong patch result")
			}
			if !tt.valid && string(p.Value) != original {
				t.Fatal("partial patch committed")
			}
			if tt.valid && (!strings.Contains(string(p.Value), "9007199254740993") || strings.Contains(string(p.Value), `"old"`)) {
				t.Fatal("patch lost precision or delete")
			}
		})
	}
}
func TestTaskTreeAndActiveLineage(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if len(s.Snapshot().Tasks.Records) != 0 {
		t.Fatal("tasks not initially empty")
	}
	root, err := s.Push(ctx, "root", "input")
	if err != nil {
		t.Fatal(err)
	}
	s.BeginCycle(ctx)
	first, _ := s.Push(ctx, "child one", "")
	s.BeginCycle(ctx)
	s.Complete(ctx, "first result")
	second, _ := s.Push(ctx, "child two", "")
	grand, _ := s.Push(ctx, "grandchild", "")
	snap := s.Snapshot()
	if !reflect.DeepEqual(snap.Tasks.Stack, []string{root, second, grand}) || snap.Tasks.Records[first].ParentID != root || snap.Tasks.Records[second].ParentID != root || snap.Tasks.Records[first].Status != "completed" {
		t.Fatal("tree/lineage mismatch")
	}
	s.Complete(ctx, "grand result")
	s.Complete(ctx, "second result")
	s.BeginCycle(ctx)
	active, ok := s.Active()
	if !ok || active.ID != root || active.Cycles != 2 || s.Snapshot().Intrinsic.GlobalCycle != 3 {
		t.Fatal("resume or counters")
	}
	if err := s.Complete(ctx, "done"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Active(); ok {
		t.Fatal("root still active")
	}
	if len(s.Snapshot().Tasks.Records) != 4 {
		t.Fatal("completed records lost")
	}
	if _, err := s.Push(ctx, "another root", ""); err == nil {
		t.Fatal("second root accepted")
	}
}
func TestTaskTerminalStates(t *testing.T) {
	for _, status := range []string{"failed", "cancelled", "incomplete"} {
		s := newStore(t)
		s.Push(context.Background(), "root", "")
		s.Push(context.Background(), "child", "")
		if err := s.End(context.Background(), status); err != nil {
			t.Fatal(err)
		}
		snap := s.Snapshot()
		if len(snap.Tasks.Stack) != 0 {
			t.Fatal("active terminal lineage")
		}
		for _, task := range snap.Tasks.Records {
			if task.Status != status || task.FinishedAt == nil {
				t.Fatal("missing terminal state")
			}
		}
	}
}
func TestOwnershipAndInvalidInitialization(t *testing.T) {
	m := &processor{fn: func(current json.RawMessage, e Event) (json.RawMessage, error) {
		current[0] = '!'
		e.Payload[0] = '!'
		return json.RawMessage(`{"status":"ignored"}`), nil
	}}
	initial := json.RawMessage(`1`)
	schema := json.RawMessage(`{"type":"integer"}`)
	s := newStore(t, Definition{"p", schema, initial, m})
	initial[0] = '!'
	schema[0] = '!'
	emit(t, s)
	snap := s.Snapshot()
	snap.Knowledge["p"].Value[0] = '!'
	snap.Intrinsic.RunID = "forged"
	journal := s.Journal()
	journal[0].Data[0] = '!'
	if string(s.Snapshot().Knowledge["p"].Value) != "1" || s.Snapshot().Intrinsic.RunID != "test-run" || !json.Valid(s.Journal()[0].Data) {
		t.Fatal("authoritative state aliased")
	}
	for _, d := range []Definition{{"p", json.RawMessage(`{"type":"integer"}`), json.RawMessage(`"bad"`), m}, {"p", json.RawMessage(`{"type":"unknown"}`), json.RawMessage(`0`), m}} {
		if store, err := New(context.Background(), []Definition{d}, Options{}); err == nil {
			store.Close(context.Background())
			t.Fatal("invalid initialization accepted")
		}
	}
}
func TestFailureClassificationAndCancellation(t *testing.T) {
	a := &processor{fn: func(json.RawMessage, Event) (json.RawMessage, error) { return nil, &RuntimeError{Code: "trap"} }}
	s := newStore(t, Definition{"p", json.RawMessage(`true`), json.RawMessage(`0`), a})
	emit(t, s)
	if p := s.Snapshot().Knowledge["p"]; p.Metadata.LastError.Kind != "runtime_error" || p.Metadata.LastError.Code != "trap" {
		t.Fatal("runtime classification")
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	a.fn = func(json.RawMessage, Event) (json.RawMessage, error) {
		cancel()
		return nil, errors.New("secret guest diagnostic")
	}
	b := &processor{fn: func(json.RawMessage, Event) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"status":"processed"}`), nil
	}}
	s = newStore(t, Definition{"a", json.RawMessage(`true`), json.RawMessage(`0`), a}, Definition{"b", json.RawMessage(`true`), json.RawMessage(`0`), b})
	_, err := s.Admit(ctx, Source{"runtime", "test"}, json.RawMessage(`{}`))
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("cancellation did not stop delivery")
	}
	if strings.Contains(string(s.JSON()), "secret") {
		t.Fatal("unsafe error exposed")
	}
}
func TestResultEnvelopeRejectsAmbiguity(t *testing.T) {
	for _, data := range []string{`{}`, `{"status":"noop"}`, `{"status":"processed","replace":1}`, `{"status":"mutation"}`, `{"status":"mutation","replace":1,"patch":[]}`, `{"status":"error","message":"secret"}`, `{"status":"ignored","status":"processed"}`, `{"status":"ignored"} {}`} {
		if _, err := decodeResult([]byte(data)); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}
