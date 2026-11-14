package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

// Retry inference only. External tool execution is owned by the provider and
// is never replayed by this wrapper. Every attempt keeps normal tracing/usage.
func retryModelTimeout[T any](ctx context.Context, status *terminalStatus, call func() (T, error)) (T, error) {
	delay := time.Second
	for {
		if err := ctx.Err(); err != nil {
			var zero T
			return zero, err
		}
		result, err := call()
		if ctx.Err() != nil {
			var zero T
			return zero, ctx.Err()
		}
		var timeout net.Error
		var provider *openai.ProviderRequestError
		retryable := errors.Is(err, context.DeadlineExceeded) || errors.Is(err, openai.ErrRequestProcessing) || errors.Is(err, openai.ErrInvalidModelResponse) ||
			(errors.As(err, &timeout) && timeout.Timeout()) ||
			(errors.As(err, &provider) && (provider.StatusCode == 429 || provider.StatusCode >= 500))
		if err == nil || !retryable {
			return result, err
		}
		if logErr := status.log("Transient model request failure; retrying in %s with retained run state.", delay); logErr != nil {
			var zero T
			return zero, logErr
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			var zero T
			return zero, ctx.Err()
		case <-timer.C:
		}
		delay = min(30*time.Second, delay*2)
	}
}

func (c observedClient) Prompt(ctx context.Context, prompt string) (openai.TextResult, error) {
	return retryModelTimeout(ctx, c.status, func() (openai.TextResult, error) { return c.promptOnce(ctx, prompt) })
}

func (c observedClient) RequestMutation(ctx context.Context, instructions string, state, observation json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
	return retryModelTimeout(ctx, c.status, func() (openai.JSONResult, error) {
		return c.requestMutationOnce(ctx, instructions, state, observation, spec)
	})
}

func (c observedClient) PromptWithSpecification(ctx context.Context, instructions, prompt string, input json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
	return retryModelTimeout(ctx, c.status, func() (openai.JSONResult, error) {
		return c.promptWithSpecificationOnce(ctx, instructions, prompt, input, spec)
	})
}

func (c observedClient) GenerateWithTools(ctx context.Context, request toolcall.Request) (toolcall.Response, error) {
	return retryModelTimeout(ctx, c.status, func() (toolcall.Response, error) { return c.generateWithToolsOnce(ctx, request) })
}
