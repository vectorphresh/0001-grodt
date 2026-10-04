package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
)

func TestContinuationReplacesPriorCycleContext(t *testing.T) {
	cycles := 0
	c := testClient{
		prompt: func(_ context.Context, p string) (openai.TextResult, error) {
			cycles++
			if cycles > 1 && !strings.Contains(p, fmt.Sprintf("Previous response:\nresponse_%d", cycles-1)) {
				t.Fatal("latest response missing")
			}
			for old := 1; old < cycles-1; old++ {
				if strings.Contains(p, fmt.Sprintf("response_%d", old)) || strings.Contains(p, fmt.Sprintf("guidance_%d", old)) {
					t.Fatal("obsolete cycle context retained")
				}
			}
			return openai.TextResult{Text: fmt.Sprintf("response_%d", cycles)}, nil
		},
		mutation: func(_ context.Context, _ string, input, _ json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
			var data struct {
				Context []string `json:"context"`
			}
			if err := json.Unmarshal(input, &data); err != nil {
				t.Fatal(err)
			}
			if len(data.Context) > 2 {
				t.Fatal("evaluation retained historical guidance")
			}
			return evaluationResult(cycles == 5, fmt.Sprintf("guidance_%d", cycles)), nil
		},
	}
	var out, diag bytes.Buffer
	if err := execute(context.Background(), "goal", "request", c, &out, &terminalStatus{writer: &diag}); err != nil {
		t.Fatal(err)
	}
	if cycles != 5 {
		t.Fatal("continuation stopped early")
	}
}

func TestContinuationBoundsSnapshotEcho(t *testing.T) {
	text := strings.Repeat("界", 10000)
	got := continuationText(text)
	if !strings.HasPrefix(got, strings.Repeat("界", 4096)) || !strings.Contains(got, "truncated") || len([]rune(got)) > 4200 {
		t.Fatal("temporary context not bounded safely")
	}
}
