// Package openai provides isolated LLM interactions over an OpenAI-compatible
// endpoint. It proposes structured results without interpreting or applying them.
package openai

import (
	"context"
	"encoding/json"
	"time"
)

// Config is supplied explicitly by the caller; this package reads no environment
// variables or configuration files. A client may be reused concurrently.
type Config struct {
	// BaseURL is the required absolute HTTP(S) API root, including any /v1 prefix.
	BaseURL string
	// APIKey is required and must not be blank. It is sent as a bearer credential.
	APIKey string
	// Model is optional. Blank selects the endpoint's default model.
	Model string
	// Timeout bounds an entire interaction. Zero selects two minutes; negative
	// values are invalid. An earlier caller deadline always takes precedence.
	Timeout time.Duration
}

// JSONSpecification is a generation contract, not local semantic validation.
// Schema is passed unchanged in meaning and checked only for valid JSON.
// Strict is forwarded to the provider; false need not disable its constraints.
type JSONSpecification struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema"`
	Strict      bool            `json:"strict"`
}

// Usage contains authoritative per-response token counts. No local estimates are used.
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// TextResult pairs assistant text with its usage; nil Usage means unavailable.
type TextResult struct {
	Text  string
	Usage *Usage
}

// JSONResult pairs structured JSON with its usage; nil Usage means unavailable.
type JSONResult struct {
	JSON  json.RawMessage
	Usage *Usage
}

// Client has no conversation history. Each method uses only its supplied inputs.
// Blank prompts/instructions and malformed JSON are rejected locally. Structured
// results retain their original text and are checked for JSON syntax, not schema
// or domain semantics. Errors never expose SDK types or provider response bodies.
type Client interface {
	Prompt(ctx context.Context, prompt string) (TextResult, error)
	PromptWithSpecification(ctx context.Context, instructions string, prompt string, context json.RawMessage, specification JSONSpecification) (JSONResult, error)
	// RequestMutation only proposes a mutation. It never modifies caller data.
	// A nil or empty observation is represented as JSON null.
	RequestMutation(ctx context.Context, instructions string, state json.RawMessage, observation json.RawMessage, specification JSONSpecification) (JSONResult, error)
}
