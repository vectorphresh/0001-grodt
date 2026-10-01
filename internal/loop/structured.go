package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/stateflow"
	"github.com/vectorphresh/0001-grodt/internal/structured"
)

// StructuredProvider generates a validated response under a caller-supplied
// contract. Dispatch and domain effects belong to the caller, not this provider.
type StructuredProvider struct {
	client        openai.Client
	instructions  string
	specification openai.JSONSpecification
}

func NewStructuredProvider(client openai.Client, instructions string, specification openai.JSONSpecification) *StructuredProvider {
	specification.Schema = bytes.Clone(specification.Schema)
	return &StructuredProvider{client: client, instructions: instructions, specification: specification}
}

func (p *StructuredProvider) Handle(ctx context.Context, state *State) (handled bool, opErr error) {
	if p == nil || p.client == nil {
		return false, errors.New("structured provider requires a client")
	}
	if ctx == nil {
		return false, errors.New("structured provider requires a context")
	}
	if state == nil || strings.TrimSpace(state.Prompt) == "" {
		return false, errors.New("structured provider requires a nonblank prompt")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	input, err := json.Marshal(struct {
		Objective string   `json:"objective"`
		Context   []string `json:"context"`
	}{state.Objective, state.Context})
	if err != nil {
		return false, errors.New("cannot encode structured input")
	}
	result, err := structured.Generate(ctx, p.specification.Schema, func(ctx context.Context, feedback json.RawMessage) (openai.JSONResult, error) {
		return p.client.PromptWithSpecification(ctx, structured.WithFeedback(p.instructions, feedback), state.Prompt, input, p.specification)
	})
	if result.Requests > 0 {
		if observed := stateflow.Observe(ctx, p.client, "structured_generation", result.JSON, err); observed != nil {
			return false, observed
		}
	}
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	state.Response = string(result.JSON)
	return true, nil
}
