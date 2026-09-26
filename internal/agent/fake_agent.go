package agent

import (
	"context"
	"fmt"
)

// FakeAgent records inputs so tests can verify feedback across steps.
type FakeAgent struct {
	Decisions []Decision
	Inputs    []StepInput
}

func NewFakeAgent(decisions []Decision) *FakeAgent { return &FakeAgent{Decisions: decisions} }
func (a *FakeAgent) Decide(ctx context.Context, input StepInput) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	a.Inputs = append(a.Inputs, input)
	if len(a.Decisions) == 0 {
		return Decision{}, fmt.Errorf("no more decisions available")
	}
	decision := a.Decisions[0]
	a.Decisions = a.Decisions[1:]
	return decision, nil
}
