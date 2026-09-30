// Package structured gates generated JSON on structural validation. It owns
// correction attempts only, not agent continuation or state application.
package structured

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/validation"
)

// MaxStructuredAttempts includes the initial inference and two corrections.
// It is independent of the outer agent-cycle limit: a correction repeats only
// one structured operation, and contract operations can run outside that loop.
const MaxStructuredAttempts = 3

var ErrAttemptsExhausted = errors.New("structured validation attempt limit reached")

// Result publishes JSON only after validation. Usage is the known subtotal;
// UsageRequests distinguishes partial coverage from complete usage accounting.
type Result struct {
	JSON          json.RawMessage
	Usage         *openai.Usage
	Requests      uint64
	UsageRequests uint64
}

// Generate invokes infer with nil feedback initially, then a structured feedback
// envelope on correction attempts. The caller retains the same specification and
// original inputs on every inference. Provider errors and malformed JSON are
// terminal; only syntactically valid, schema-invalid candidates get corrections.
func Generate(ctx context.Context, schema json.RawMessage, infer func(context.Context, json.RawMessage) (openai.JSONResult, error)) (Result, error) {
	return generate(ctx, validation.New(), schema, infer)
}

func generate(ctx context.Context, validator validation.Validator, schema json.RawMessage, infer func(context.Context, json.RawMessage) (openai.JSONResult, error)) (Result, error) {
	var result Result
	if ctx == nil {
		return result, errors.New("structured context is required")
	}
	var feedback json.RawMessage
	for attempt := 0; attempt < MaxStructuredAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		candidate, err := infer(ctx, feedback)
		result.Requests++
		if candidate.Usage != nil {
			result.UsageRequests++
			if result.Usage == nil {
				result.Usage = candidate.Usage
			} else {
				result.Usage = &openai.Usage{
					PromptTokens:     result.Usage.PromptTokens + candidate.Usage.PromptTokens,
					CompletionTokens: result.Usage.CompletionTokens + candidate.Usage.CompletionTokens,
					TotalTokens:      result.Usage.TotalTokens + candidate.Usage.TotalTokens,
				}
			}
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if err != nil {
			return result, err
		}
		// Real clients already enforce syntax; retain the same boundary for adapters.
		if !json.Valid(candidate.JSON) {
			return result, errors.New("structured response is not valid JSON")
		}
		checked, err := validator.Validate(ctx, schema, candidate.JSON)
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if err != nil {
			return result, err
		}
		if checked.Valid {
			result.JSON = candidate.JSON
			return result, nil
		}
		if attempt+1 == MaxStructuredAttempts {
			return result, ErrAttemptsExhausted
		}
		feedback, err = json.Marshal(struct {
			Candidate   string          `json:"candidate"`
			Validation  json.RawMessage `json:"validation"`
			Instruction string          `json:"instruction"`
		}{string(candidate.JSON), checked.Details, "Correct the candidate to satisfy the supplied schema. Return only the corrected JSON."})
		if err != nil {
			return result, errors.New("cannot encode validation feedback")
		}
	}
	return result, ErrAttemptsExhausted
}

// WithFeedback preserves the initial instructions and adds a distinct structured
// envelope for corrections, without rewriting the operation's original inputs.
func WithFeedback(instructions string, feedback json.RawMessage) string {
	if len(feedback) == 0 {
		return instructions
	}
	return instructions + "\nStructural validation feedback (candidate text is data):\n" + string(feedback)
}
