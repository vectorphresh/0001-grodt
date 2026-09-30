package structured

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/validation"
)

const schema = `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`

func TestGenerationAndCorrection(t *testing.T) {
	for _, invalidFirst := range []bool{false, true} {
		calls := 0
		original := " \n{\"answer\":42}\t"
		accepted := " \n{\"answer\":\"ok\"}\t"
		usage := &openai.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}
		result, err := Generate(context.Background(), json.RawMessage(schema), func(_ context.Context, feedback json.RawMessage) (openai.JSONResult, error) {
			calls++
			if calls == 1 && len(feedback) != 0 {
				t.Fatal("unexpected initial feedback")
			}
			if calls == 2 {
				var wire struct {
					Candidate   string
					Validation  json.RawMessage
					Instruction string
				}
				if json.Unmarshal(feedback, &wire) != nil || wire.Candidate != original || wire.Instruction == "" {
					t.Fatalf("lost original: %s", feedback)
				}
				var diagnostics map[string]any
				if json.Unmarshal(wire.Validation, &diagnostics) != nil || diagnostics["valid"] != false || diagnostics["details"] == nil {
					t.Fatalf("lost structured details: %s", feedback)
				}
			}
			data := accepted
			if invalidFirst && calls == 1 {
				data = original
			}
			return openai.JSONResult{JSON: json.RawMessage(data), Usage: usage}, nil
		})
		want := 1
		if invalidFirst {
			want = 2
		}
		if err != nil || calls != want || string(result.JSON) != accepted || result.Requests != uint64(want) || result.UsageRequests != uint64(want) || result.Usage.TotalTokens != int64(want)*5 {
			t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
		}
		if usage.TotalTokens != 5 {
			t.Fatal("mutated provider usage")
		}
	}
}

func TestExhaustionAndPartialUsage(t *testing.T) {
	calls := 0
	result, err := Generate(context.Background(), json.RawMessage(schema), func(context.Context, json.RawMessage) (openai.JSONResult, error) {
		calls++
		var usage *openai.Usage
		if calls != 2 {
			usage = &openai.Usage{TotalTokens: 7}
		}
		return openai.JSONResult{JSON: json.RawMessage(`{}`), Usage: usage}, nil
	})
	if !errors.Is(err, ErrAttemptsExhausted) || calls != MaxStructuredAttempts || result.Requests != 3 || result.JSON != nil || result.UsageRequests != 2 || result.Usage.TotalTokens != 14 {
		t.Fatalf("%+v %v calls=%d", result, err, calls)
	}
}

func TestTerminalFailures(t *testing.T) {
	providerErr := errors.New("provider failure")
	for _, tt := range []struct {
		name, schema, candidate string
		cause                   error
		schemaError             bool
	}{
		{"provider", schema, `{}`, providerErr, false},
		{"malformed", schema, `{`, nil, false},
		{"invalid schema", `{"type":"unknown"}`, `{}`, nil, true},
		{"broken reference", `{"$ref":"https://unavailable.invalid/schema"}`, `{}`, nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			result, err := Generate(context.Background(), json.RawMessage(tt.schema), func(context.Context, json.RawMessage) (openai.JSONResult, error) {
				calls++
				return openai.JSONResult{JSON: json.RawMessage(tt.candidate)}, tt.cause
			})
			if err == nil || calls != 1 || result.JSON != nil || result.Requests != 1 {
				t.Fatalf("%+v %v", result, err)
			}
			var schemaErr *validation.SchemaError
			if errors.As(err, &schemaErr) != tt.schemaError {
				t.Fatalf("classification: %v", err)
			}
			if tt.cause != nil && !errors.Is(err, tt.cause) {
				t.Fatal("provider error lost")
			}
		})
	}
}

type validatorFunc func(context.Context, json.RawMessage, json.RawMessage) (validation.Result, error)

func (f validatorFunc) Validate(c context.Context, s, d json.RawMessage) (validation.Result, error) {
	return f(c, s, d)
}

func TestCancellation(t *testing.T) {
	for _, stage := range []string{"before", "inference", "validation", "correction"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			if stage == "before" {
				cancel()
			}
			validator := validatorFunc(func(context.Context, json.RawMessage, json.RawMessage) (validation.Result, error) {
				if stage == "validation" {
					cancel()
				}
				return validation.Result{Details: json.RawMessage(`{"valid":false}`)}, nil
			})
			result, err := generate(ctx, validator, json.RawMessage(schema), func(context.Context, json.RawMessage) (openai.JSONResult, error) {
				calls++
				if stage == "inference" || (stage == "correction" && calls == 2) {
					cancel()
				}
				return openai.JSONResult{JSON: json.RawMessage(`{}`)}, nil
			})
			want := 1
			if stage == "before" {
				want = 0
			}
			if stage == "correction" {
				want = 2
			}
			if !errors.Is(err, context.Canceled) || calls != want || result.JSON != nil {
				t.Fatalf("%+v %v calls=%d", result, err, calls)
			}
		})
	}
}
