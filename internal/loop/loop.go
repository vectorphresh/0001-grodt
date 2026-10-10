// Package loop provides synchronous provider-chain traversal. Application input,
// history, continuation, and retry policy belong to the caller.
package loop

import (
	"context"
	"errors"
	"fmt"
)

// State is the mutable working set for one orchestration cycle. It is not
// persistent or domain state. The caller must discard failed cycles.
type State struct {
	// Objective is owned by the application and read-only to providers.
	Objective string
	Prompt    string
	Context   []string
	Response  string
	// ObjectiveEvaluation is set only after the root evaluator confirms completion
	// at a plan boundary. The outer loop consumes it without evaluating twice.
	ObjectiveEvaluation *ObjectiveEvaluation
}

type ObjectiveEvaluation struct {
	Achieved  bool
	Rationale string
}

// Provider contributes context and falls through, handles the request, or fails.
// Handle must not return while any operation it initiated remains in flight.
// Operations must honor ctx. Providers must not detach background work.
type Provider interface {
	Handle(ctx context.Context, state *State) (handled bool, err error)
}

// ErrUnhandled indicates that no provider consumed the current request.
var ErrUnhandled = errors.New("request was not handled")

// Process traverses providers in order, synchronously, without retries. An error
// or cancellation aborts traversal. State changes are not rolled back; the caller
// must not use a failed cycle's partial response or infer partial success.
// Callers must serialize cycles that share state or providers.
func Process(ctx context.Context, state *State, providers []Provider) error {
	if ctx == nil {
		return errors.New("process context is required")
	}
	if state == nil {
		return errors.New("process state is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for i, provider := range providers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if provider == nil {
			return fmt.Errorf("provider %d is nil", i)
		}
		handled, err := provider.Handle(ctx, state)
		if err != nil {
			return fmt.Errorf("provider %d: %w", i, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if handled {
			return nil
		}
	}
	return ErrUnhandled
}
