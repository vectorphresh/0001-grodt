package contracts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/structured"
)

const reductionInstructions = "Inspect current state, new information, and semantic context. Produce proposed JSON using only the supplied optional contract properties where the information warrants a proposal. Preserve the meaning of authoritative observations; do not invent facts. An empty object is allowed. The schema constrains proposal shape only: do not infer replacement, merge, patch, deletion, or application semantics from presence or omission."

func (r *Reducer) Reduce(ctx context.Context, input Input, names []string) (Mutation, error) {
	if err := checkContext(ctx); err != nil {
		return Mutation{}, err
	}
	if !json.Valid(input.State) {
		return Mutation{}, errors.New("contracts: state must contain valid JSON")
	}
	if !json.Valid(input.Information) {
		return Mutation{}, errors.New("contracts: information must contain valid JSON")
	}
	semanticContext, err := optionalContext(input.Context)
	if err != nil {
		return Mutation{}, err
	}
	spec, err := r.PartialSpecification(names)
	if err != nil {
		return Mutation{}, err
	}
	writable := false
	for _, name := range names {
		if r.byName[name].ModelWritable {
			writable = true
			break
		}
	}
	if !writable {
		return Mutation{JSON: json.RawMessage(`{}`)}, nil
	}
	observation, err := json.Marshal(struct {
		Information json.RawMessage `json:"information"`
		Context     json.RawMessage `json:"context"`
	}{input.Information, semanticContext})
	if err != nil {
		return Mutation{}, errors.New("contracts: cannot encode reduction input")
	}
	result, err := structured.Generate(ctx, spec.Schema, func(ctx context.Context, feedback json.RawMessage) (openai.JSONResult, error) {
		candidate, err := r.client.RequestMutation(ctx, structured.WithFeedback(reductionInstructions, feedback), input.State, observation, spec)
		if err != nil {
			return candidate, operationError(ctx, err)
		}
		return candidate, nil
	})
	mutation := Mutation{Requests: result.Requests, Usage: result.Usage, UsageRequests: result.UsageRequests}
	if err != nil {
		return mutation, err
	}
	if err := checkContext(ctx); err != nil {
		return mutation, err
	}
	data := bytes.TrimSpace(result.JSON)
	if !json.Valid(data) || data[0] != '{' {
		return mutation, errors.New("contracts: proposal must be a JSON object")
	}
	// Return validated original bytes. Authorization and application remain caller decisions.
	mutation.JSON = result.JSON
	return mutation, nil
}

func optionalContext(data json.RawMessage) (json.RawMessage, error) {
	if len(data) == 0 {
		return json.RawMessage("null"), nil
	}
	if !json.Valid(data) {
		return nil, errors.New("contracts: context must contain valid JSON")
	}
	return data, nil
}
func checkContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("contracts: context is required")
	}
	return ctx.Err()
}
func operationError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, cause) {
			return cause
		}
	}
	return errors.New("contracts: LLM operation failed")
}
