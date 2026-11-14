package loop

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

func TestReconciliationRepairsBeyondThirtyTwoAttempts(t *testing.T) {
	p := reconciliationFixture(t)
	seedProcedure(t, p, "inspect", "Inspect configuration")
	started := p.Store.Snapshot().Intrinsic.StartedAt
	p.repairNow = func() time.Time { return started.Add(10 * time.Minute) }
	before := p.Store.Snapshot().Tasks
	calls := 0
	p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, _ json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		if calls > 2 && !strings.Contains(instructions, "Targeted repair:") {
			t.Fatal("repeated rejection did not change repair modality")
		}
		if calls <= 40 {
			return reconciliationResult(t, "no_progress", nil, "invented change"), nil
		}
		return reconciliationResult(t, "no_progress", nil, ""), nil
	}}
	if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Inspect", nil); err != nil {
		t.Fatal(err)
	}
	if calls != 41 || p.reconciliationCheckpoint != nil || !reflect.DeepEqual(before, p.Store.Snapshot().Tasks) {
		t.Fatal("repair cut off early, retained a completed checkpoint, or changed accepted work")
	}
}

func TestReconciliationContinuesPastElapsedHour(t *testing.T) {
	p := reconciliationFixture(t)
	p.repairDeadline = time.Time{} // Application default: no run-age cutoff.
	p.repairNow = func() time.Time { return p.Store.Snapshot().Intrinsic.StartedAt.Add(2 * time.Hour) }
	before := p.Store.Snapshot().Tasks
	calls := 0
	p.Reconciler = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		if calls == 1 {
			return openai.JSONResult{}, context.DeadlineExceeded
		}
		if calls == 2 {
			return reconciliationResult(t, "no_progress", nil, "invalid change"), nil
		}
		return reconciliationResult(t, "no_progress", nil, ""), nil
	}}
	if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Inspect", nil); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || p.reconciliationCheckpoint != nil || !reflect.DeepEqual(before, p.Store.Snapshot().Tasks) {
		t.Fatal("timeout or elapsed run time prevented repair or changed accepted state")
	}
}

func TestReconciliationDeadlineUsesElapsedRunTime(t *testing.T) {
	p := reconciliationFixture(t)
	started := p.Store.Snapshot().Intrinsic.StartedAt
	calls := 0
	p.repairNow = func() time.Time {
		if calls == 0 {
			return started.Add(59 * time.Minute)
		}
		return started.Add(time.Hour)
	}
	p.Reconciler = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		return reconciliationResult(t, "no_progress", nil, "invalid change"), nil
	}}
	before := p.Store.Snapshot().Tasks
	if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Inspect", nil); !errors.Is(err, errReconciliationDeadline) {
		t.Fatalf("expected original run deadline: %v", err)
	}
	if calls != 1 || p.reconciliationCheckpoint == nil || !reflect.DeepEqual(before, p.Store.Snapshot().Tasks) {
		t.Fatal("deadline reset or rejected state accepted")
	}
	if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Inspect", nil); !errors.Is(err, errReconciliationDeadline) || calls != 1 {
		t.Fatal("reentry granted a fresh repair hour")
	}
}

func TestReconciliationInterruptedCheckpointRepairsBeforeActor(t *testing.T) {
	p := reconciliationFixture(t)
	seedProcedure(t, p, "inspect", "Inspect configuration")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	actor, repairs := 0, 0
	stop := errors.New("actor reached after repair")
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		actor++
		if actor == 1 {
			return toolcall.Response{Calls: []toolcall.Call{toolRequest(r, "query")}}, nil
		}
		return toolcall.Response{}, stop
	})
	p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		repairs++
		var input reconciliationInput
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		if len(input.AcceptedResults) != 1 {
			t.Fatal("original accepted result missing from checkpoint")
		}
		if repairs == 1 {
			cancel()
			return reconciliationResult(t, "no_progress", nil, "invalid change", input), nil
		}
		if !strings.Contains(instructions, "Rejected proposal") {
			t.Fatal("candidate lost on reentry")
		}
		return reconciliationResult(t, "no_progress", nil, "", input), nil
	}}
	if _, err := p.Handle(ctx, &State{Objective: "Inspect"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	if active.Attempts.Total != 1 || p.reconciliationCheckpoint == nil {
		t.Fatal("interruption lost execution checkpoint")
	}
	if _, err := p.Handle(context.Background(), &State{Objective: "Inspect"}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	active, _ = p.Store.Active()
	if repairs != 2 || actor != 2 || active.Attempts.Total != 1 || active.Attempts.Outcome == "in_progress" || p.reconciliationCheckpoint != nil {
		t.Fatal("resume repeated tools or failed to finish original attempt")
	}
	journal := string(mustEncode(p.Store.Journal()))
	for _, want := range []string{`"kind":"reconciliation_checkpoint"`, `"before_batch"`, `"calls"`, "invalid change"} {
		if !strings.Contains(journal, want) {
			t.Fatalf("checkpoint audit missing %s", want)
		}
	}
}

func TestReconciliationSchemaExhaustionCanRepair(t *testing.T) {
	p := reconciliationFixture(t)
	calls := 0
	p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, _ json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		if calls <= 5 {
			return openai.JSONResult{JSON: json.RawMessage(`{}`)}, nil
		}
		if !strings.Contains(instructions, "structured validation attempt limit reached") || !strings.Contains(instructions, "Rejected proposal") {
			t.Fatal("exhausted schema batch lost correction context")
		}
		return reconciliationResult(t, "no_progress", nil, ""), nil
	}}
	if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Inspect", nil); err != nil || calls != 6 {
		t.Fatalf("schema exhaustion terminated recoverable run: calls=%d err=%v", calls, err)
	}
}

func TestReconciliationRunDeadlineCancelsInflightRequest(t *testing.T) {
	store, err := runstate.New(context.Background(), nil, runstate.Options{StartedAt: time.Now().Add(-time.Hour + 100*time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	if _, err := store.Push(context.Background(), "Inspect", "Inspect"); err != nil {
		t.Fatal(err)
	}
	p := &ToolProvider{Store: store, repairDeadline: store.Snapshot().Intrinsic.StartedAt.Add(time.Hour)}
	calls := 0
	p.Reconciler = structuredClient{call: func(ctx context.Context, _, _ string, _ json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		<-ctx.Done()
		return openai.JSONResult{}, ctx.Err()
	}}
	if err := p.reconcile(context.Background(), store.Snapshot(), "Inspect", nil); !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errReconciliationDeadline) {
		t.Fatalf("in-flight request outlived run deadline: %v", err)
	}
	if calls != 1 || p.reconciliationCheckpoint == nil {
		t.Fatal("deadline lost checkpoint or issued another request")
	}
}

func TestReconciliationRequestFailureCanRepair(t *testing.T) {
	p := reconciliationFixture(t)
	calls := 0
	p.Reconciler = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		if calls == 1 {
			return openai.JSONResult{}, errors.New("temporary transport failure")
		}
		return reconciliationResult(t, "no_progress", nil, ""), nil
	}}
	if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Inspect", nil); err != nil || calls != 2 {
		t.Fatalf("request failure was terminal: calls=%d err=%v", calls, err)
	}
}

func TestReconciliationInterruptedCompletionIsRevalidated(t *testing.T) {
	p := reconciliationFixture(t)
	seedProcedure(t, p, "inspect", "Inspect configuration")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before := p.Store.Snapshot().Tasks
	p.Reconciler = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		var input reconciliationInput
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		step := input.Plan.Steps[0]
		step.Status, step.Outcome = "satisfied", "Inspection complete"
		step.Evidence = []runstate.EvidenceReference{input.AcceptedEvidence[0].Reference}
		return reconciliationResult(t, "progress", []progressUpdate{{Step: step}}, ""), nil
	}}
	evaluations := 0
	p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		evaluations++
		if evaluations == 1 {
			cancel()
		}
		return continuityResult(true, "Completion supported"), nil
	}}
	if err := p.reconcile(ctx, p.Store.Snapshot(), "Inspect", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, p.Store.Snapshot().Tasks) || p.reconciliationCheckpoint == nil || !strings.Contains(string(p.reconciliationCheckpoint.Proposed), "Inspection complete") {
		t.Fatal("interrupted completion was accepted or its candidate lost")
	}
	if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Inspect", nil); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	if evaluations != 2 || active.Plan.Steps[0].Status != "satisfied" {
		t.Fatal("resumed candidate bypassed completion evaluation")
	}
}
