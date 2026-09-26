package validation

import (
	"context"
	"fmt"

	"github.com/vectorphresh/0001-grodt/internal/agent"
	"github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/tools"
)

type Validators struct {
	Decision   DecisionValidator
	StatePatch StatePatchValidator
	Permission ActionValidator
	Action     ActionValidator
}

type DecisionValidator interface {
	Validate(context.Context, agent.Decision) error
}
type StatePatchValidator interface {
	Validate(context.Context, state.AgentState, state.StatePatch) error
}
type ActionValidator interface {
	Validate(context.Context, state.AgentState, tools.ToolCall) error
}

// NewValidators denies tool execution unless the exact public name is allowed.
// Action is an additional runtime policy hook, e.g. a risk limit validator.
func NewValidators(allowedTools ...string) *Validators {
	allowed := make(map[string]bool)
	for _, name := range allowedTools {
		allowed[name] = true
	}
	return &Validators{
		Decision:   DecisionValidatorImpl{},
		StatePatch: StatePatchValidatorImpl{},
		Permission: PermissionValidator{allowed: allowed},
		Action:     ActionValidators{},
	}
}

type DecisionValidatorImpl struct{}

func (DecisionValidatorImpl) Validate(ctx context.Context, d agent.Decision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return d.Validate()
}

type StatePatchValidatorImpl struct{}

func (StatePatchValidatorImpl) Validate(ctx context.Context, s state.AgentState, p state.StatePatch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return state.ValidatePatch(s, p)
}

type PermissionValidator struct{ allowed map[string]bool }

func (p PermissionValidator) Validate(ctx context.Context, _ state.AgentState, call tools.ToolCall) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !p.allowed[call.Name] {
		return fmt.Errorf("tool %q is not permitted", call.Name)
	}
	return nil
}

// ActionValidators runs every configured policy before execution.
type ActionValidators []ActionValidator

func (validators ActionValidators) Validate(ctx context.Context, s state.AgentState, call tools.ToolCall) error {
	for _, validator := range validators {
		if err := ctx.Err(); err != nil {
			return err
		}
		if validator == nil {
			return fmt.Errorf("nil action validator")
		}
		if err := validator.Validate(ctx, s.Clone(), call); err != nil {
			return err
		}
	}
	return ctx.Err()
}
