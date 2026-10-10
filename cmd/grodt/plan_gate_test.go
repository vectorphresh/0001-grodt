package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/config"
	"github.com/vectorphresh/0001-grodt/internal/loop"
	"github.com/vectorphresh/0001-grodt/internal/mcp"
	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

type boundaryActor func(context.Context, toolcall.Request) (toolcall.Response, error)

func (f boundaryActor) GenerateWithTools(ctx context.Context, request toolcall.Request) (toolcall.Response, error) {
	return f(ctx, request)
}

func TestRootEvaluationAtPlanBoundaryFinishesWithoutDuplicateEvaluation(t *testing.T) {
	ctx := context.Background()
	store, err := runstate.New(ctx, nil, runstate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(ctx)
	runtime, err := mcp.New(ctx, config.MCPConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	provider := &loop.ToolProvider{Store: store, Runtime: runtime, Client: boundaryActor(func(context.Context, toolcall.Request) (toolcall.Response, error) {
		t.Fatal("actor ran after the root evaluator confirmed completion")
		return toolcall.Response{}, nil
	})}
	checks := 0
	evaluator := func(_ context.Context, _ openai.Client, objective, initial string, _ loop.State) (GoalEvaluation, error) {
		checks++
		if objective != "Root goal" || initial != "Original request" {
			t.Fatal("plan boundary evaluated a substituted goal")
		}
		return GoalEvaluation{Achieved: true, Rationale: "Goal established."}, nil
	}
	var output, diagnostics bytes.Buffer
	if err := runObjectiveWithEvaluator(ctx, "Root goal", "Original request", []loop.Provider{provider}, nil, &output, &terminalStatus{writer: &diagnostics}, &runMetrics{}, store, evaluator); err != nil {
		t.Fatal(err)
	}
	if checks != 1 || output.String() != "Goal established.\n" || provider.CheckObjective != nil {
		t.Fatal("duplicate evaluation, incorrect output, or leaked callback")
	}
	for _, task := range store.Snapshot().Tasks.Records {
		if task.ParentID == "" && task.Status != "completed" {
			t.Fatal("root objective not completed normally")
		}
	}
}
