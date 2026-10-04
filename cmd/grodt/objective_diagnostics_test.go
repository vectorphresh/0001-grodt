package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/loop"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
)

func TestToolFailureStageReachesRunReport(t *testing.T) {
	ctx := context.Background()
	store, err := runstate.New(ctx, nil, runstate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(ctx)
	var output, diagnostics bytes.Buffer
	err = runObjective(ctx, "objective", "request", []loop.Provider{&loop.ToolProvider{}}, nil,
		&output, &terminalStatus{writer: &diagnostics}, &runMetrics{}, store)
	if err == nil || !strings.Contains(err.Error(), "tool operation failed during initialization") {
		t.Fatalf("missing failure stage: %v", err)
	}
	if output.Len() != 0 || !strings.Contains(diagnostics.String(), "Rationale: tool operation failed during initialization; cycle discarded.") {
		t.Fatalf("incorrect failure report: %s", diagnostics.String())
	}
}
