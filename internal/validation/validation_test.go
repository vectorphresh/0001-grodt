package validation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/tools"
	"github.com/vectorphresh/0001-grodt/internal/validation"
)

type actionFunc func(context.Context, state.AgentState, tools.ToolCall) error

func (f actionFunc) Validate(c context.Context, s state.AgentState, call tools.ToolCall) error {
	return f(c, s, call)
}

func TestPermissionsDenyByDefaultAndMatchExactNames(t *testing.T) {
	ctx := context.Background()
	call := tools.ToolCall{Name: "fake.read"}
	if err := validation.NewValidators().Permission.Validate(ctx, state.AgentState{}, call); err == nil {
		t.Fatal("default permission allowed tool")
	}
	allowed := validation.NewValidators("fake.read")
	if err := allowed.Permission.Validate(ctx, state.AgentState{}, call); err != nil {
		t.Fatal(err)
	}
	call.Name = "other.read"
	if err := allowed.Permission.Validate(ctx, state.AgentState{}, call); err == nil {
		t.Fatal("permission crossed providers")
	}
}

func TestActionPoliciesStopOnRejection(t *testing.T) {
	calls := 0
	sentinel := errors.New("risk rejected")
	policies := validation.ActionValidators{
		actionFunc(func(context.Context, state.AgentState, tools.ToolCall) error { calls++; return nil }),
		actionFunc(func(context.Context, state.AgentState, tools.ToolCall) error { calls++; return sentinel }),
		actionFunc(func(context.Context, state.AgentState, tools.ToolCall) error { calls++; return nil }),
	}
	if err := policies.Validate(context.Background(), state.AgentState{}, tools.ToolCall{}); !errors.Is(err, sentinel) || calls != 2 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}
