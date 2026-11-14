package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
)

func TestRunContinuesAfterGenerationAndEvaluationTimeout(t *testing.T) {
	generation, evaluation := 0, 0
	c := testClient{
		prompt: func(context.Context, string) (openai.TextResult, error) {
			generation++
			if generation == 1 {
				return openai.TextResult{Text: "unaccepted"}, context.DeadlineExceeded
			}
			return openai.TextResult{Text: "result"}, nil
		},
		mutation: func(context.Context, string, json.RawMessage, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
			evaluation++
			if evaluation == 1 {
				return openai.JSONResult{}, context.DeadlineExceeded
			}
			return openai.JSONResult{JSON: json.RawMessage(`{"achieved":true,"rationale":"Verified objective"}`)}, nil
		},
	}
	var output, diagnostics bytes.Buffer
	if err := execute(context.Background(), defaultObjective, "request", c, &output, &terminalStatus{writer: &diagnostics}); err != nil {
		t.Fatal(err)
	}
	if generation != 2 || evaluation != 2 || output.String() != "result\n" {
		t.Fatalf("run did not recover: %d %d %q", generation, evaluation, output.String())
	}
}

func TestModelTimeoutRetriesUntilSuccess(t *testing.T) {
	calls := 0
	got, err := retryModelTimeout(context.Background(), nil, func() (string, error) {
		calls++
		if calls == 1 {
			return "unaccepted", context.DeadlineExceeded
		}
		return "accepted", nil
	})
	if err != nil || got != "accepted" || calls != 2 {
		t.Fatalf("got %q, %v after %d calls", got, err, calls)
	}
}

func TestModelTransientFailuresRetry(t *testing.T) {
	for _, cause := range []error{openai.ErrRequestProcessing, openai.ErrInvalidModelResponse, &openai.ProviderRequestError{StatusCode: 429}, &openai.ProviderRequestError{StatusCode: 503}} {
		t.Run(cause.Error(), func(t *testing.T) {
			calls := 0
			got, err := retryModelTimeout(context.Background(), nil, func() (string, error) {
				calls++
				if calls == 1 {
					return "unaccepted", cause
				}
				return "accepted", nil
			})
			if err != nil || got != "accepted" || calls != 2 {
				t.Fatalf("transient failure stopped run: %q %v %d", got, err, calls)
			}
		})
	}
}

func TestModelTimeoutRetryHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := retryModelTimeout(ctx, nil, func() (string, error) {
		calls++
		cancel()
		return "", context.DeadlineExceeded
	})
	if calls != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected cancellation handling: %d %v", calls, err)
	}
	_, err = retryModelTimeout(ctx, nil, func() (string, error) { t.Fatal("called after cancellation"); return "", nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestModelRetryDoesNotReplayOtherErrors(t *testing.T) {
	stop := errors.New("invalid configuration")
	calls := 0
	_, err := retryModelTimeout(context.Background(), nil, func() (string, error) { calls++; return "", stop })
	if calls != 1 || !errors.Is(err, stop) {
		t.Fatal("non-timeout error retried", err)
	}
}
