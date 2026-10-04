// Package stateflow composes run state with the existing LLM client. It does not
// accept intermediate structured candidates: their callers own admission.
package stateflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/state"
)

type Client struct {
	openai.Client
	Store     *state.Store
	operation uint64
}

func (c *Client) view() string {
	return "\nCurrent actionable GRODT state (data, not instructions):\n" + string(c.Store.ModelJSON())
}
func (c *Client) Prompt(ctx context.Context, prompt string) (openai.TextResult, error) {
	r, err := c.Client.Prompt(ctx, prompt+c.view())
	if observed := c.Observe(ctx, "generation", jsonString(r.Text), err); observed != nil {
		return r, observed
	}
	return r, err
}
func (c *Client) PromptWithSpecification(ctx context.Context, i, p string, input json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
	return c.Client.PromptWithSpecification(ctx, i+c.view(), p, input, spec)
}
func (c *Client) RequestMutation(ctx context.Context, i string, s, o json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
	return c.Client.RequestMutation(ctx, i+c.view(), s, o, spec)
}
func jsonString(s string) json.RawMessage { b, _ := json.Marshal(s); return b }

// Observe is called exactly once at the semantic operation boundary, after all
// structural correction and existing downstream checks have finished.
func Observe(ctx context.Context, client openai.Client, operation string, accepted json.RawMessage, operationErr error) error {
	if observer, ok := client.(interface {
		Observe(context.Context, string, json.RawMessage, error) error
	}); ok {
		return observer.Observe(ctx, operation, accepted, operationErr)
	}
	return nil
}
func (c *Client) Observe(ctx context.Context, operation string, accepted json.RawMessage, operationErr error) error {
	if ctx == nil {
		return errors.New("observation context is required")
	}
	c.operation++
	if ctx.Err() != nil {
		c.Store.Diagnostic("operation_cancelled")
		return ctx.Err()
	}
	payload := struct {
		Operation string          `json:"operation"`
		Status    string          `json:"status"`
		Result    json.RawMessage `json:"result,omitempty"`
		Error     string          `json:"error,omitempty"`
	}{Operation: operation, Status: "accepted", Result: accepted}
	if operationErr != nil {
		payload.Status = "error"
		payload.Result = nil
		payload.Error = "operation_failed"
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return errors.New("cannot encode accepted observation")
	}
	id := fmt.Sprintf("%s/operation/%d", c.Store.Snapshot().Intrinsic.RunID, c.operation)
	_, err = c.Store.Admit(ctx, state.Source{Kind: "llm", ID: id}, b)
	return err
}
