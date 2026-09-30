package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/structured"
)

func TestEvaluationValidationCorrection(t *testing.T) {
	for _, correct := range []bool{true, false} {
		generation, evaluations := 0, 0
		c := testClient{
			prompt: func(context.Context, string) (openai.TextResult, error) {
				generation++
				return openai.TextResult{Text: "answer", Usage: &openai.Usage{TotalTokens: 2}}, nil
			},
			mutation: func(_ context.Context, instructions string, state, observation json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
				evaluations++
				if evaluations > 1 && (!strings.Contains(instructions, `"candidate":"{}`) || !strings.Contains(instructions, `"validation":{`)) {
					t.Fatal("missing structured feedback")
				}
				if string(spec.Schema) != evaluationSchema || string(observation) != `{"purpose":"goal_evaluation"}` {
					t.Fatal("operation inputs changed")
				}
				candidate := `{}`
				if correct && evaluations == 2 {
					candidate = `{"achieved":true,"rationale":"done"}`
				}
				return openai.JSONResult{JSON: json.RawMessage(candidate), Usage: &openai.Usage{TotalTokens: 3}}, nil
			},
		}
		var out, diag bytes.Buffer
		err := execute(context.Background(), "goal", "request", c, &out, &terminalStatus{writer: &diag})
		if generation != 1 {
			t.Fatal("correction reran generation")
		}
		if correct {
			if err != nil || evaluations != 2 || out.String() != "answer\n" || !strings.Contains(diag.String(), "Requests: 3\n") || !strings.Contains(diag.String(), "Total tokens: 8\n") || !strings.Contains(diag.String(), "Usage coverage: 3/3") {
				t.Fatalf("calls=%d err=%v report=%s", evaluations, err, &diag)
			}
		} else {
			if err == nil || evaluations != structured.MaxStructuredAttempts || out.Len() != 0 || !strings.Contains(diag.String(), "Status: failed") || !strings.Contains(diag.String(), "Requests: 4\n") {
				t.Fatalf("calls=%d err=%v report=%s", evaluations, err, &diag)
			}
		}
	}
}
