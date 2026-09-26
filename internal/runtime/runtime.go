package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/agent"
	"github.com/vectorphresh/0001-grodt/internal/observation"
	"github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/tools"
	"github.com/vectorphresh/0001-grodt/internal/validation"
)

// Reducer is trusted runtime code. Only successful, schema-validated tool
// observations are delivered to reducers. No provider-specific logic lives here.
type Reducer interface {
	Supports(observation.Observation) bool
	Reduce(state.AgentState, observation.Observation) (state.AgentState, error)
}

type Option func(*Runtime)

func WithClock(now func() time.Time) Option { return func(r *Runtime) { r.now = now } }
func WithReducers(reducers ...Reducer) Option {
	return func(r *Runtime) { r.reducers = append(r.reducers, reducers...) }
}

// Runtime serializes steps. Observations are transient feedback, never implicitly
// inserted into working memory. A Store should have only one active Runtime writer.
type Runtime struct {
	mu                 sync.Mutex
	stateStore         state.Store
	agent              agent.Agent
	toolRegistry       tools.Registry
	validators         *validation.Validators
	reducers           []Reducer
	currentObservation *observation.Observation
	now                func() time.Time
	stepID             uint64
}

func NewRuntime(store state.Store, decider agent.Agent, registry tools.Registry, validators *validation.Validators, options ...Option) *Runtime {
	r := &Runtime{stateStore: store, agent: decider, toolRegistry: registry, validators: validators, now: time.Now}
	for _, option := range options {
		option(r)
	}
	return r
}

type StepResult struct {
	Done        bool
	Decision    agent.Decision
	Observation *observation.Observation
}

func cloneObservation(obs *observation.Observation) *observation.Observation {
	if obs == nil {
		return nil
	}
	copy := *obs
	copy.Data = append([]byte(nil), obs.Data...)
	if obs.Error != nil {
		e := *obs.Error
		copy.Error = &e
	}
	return &copy
}

func (r *Runtime) Step(ctx context.Context) (StepResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return StepResult{}, err
	}
	if r.stateStore == nil || r.agent == nil || r.toolRegistry == nil || r.now == nil ||
		r.validators == nil || r.validators.Decision == nil || r.validators.StatePatch == nil ||
		r.validators.Permission == nil || r.validators.Action == nil {
		return StepResult{}, fmt.Errorf("runtime dependencies and validators must be configured")
	}
	for _, reducer := range r.reducers {
		if reducer == nil {
			return StepResult{}, fmt.Errorf("nil reducer")
		}
	}
	current, err := r.stateStore.Load(ctx)
	if err != nil {
		return StepResult{}, fmt.Errorf("load state: %w", err)
	}
	if current == nil {
		return StepResult{}, fmt.Errorf("store returned nil state")
	}
	definitions, err := r.toolRegistry.List(ctx)
	if err != nil {
		return StepResult{}, fmt.Errorf("list tools: %w", err)
	}
	r.stepID++
	newObservation := func(source, tool string, data []byte, failure *observation.ErrorInfo) *observation.Observation {
		return &observation.Observation{
			ID: fmt.Sprintf("obs_%d", r.stepID), Source: source, Tool: tool,
			Timestamp: r.now().UTC(), Data: append([]byte(nil), data...), Error: failure,
		}
	}
	// Commit once per completed step, including recoverable failure and Done.
	// The original patch is rolled back on validation failure.
	finish := func(next state.AgentState, decision agent.Decision, obs *observation.Observation) (StepResult, error) {
		if err := ctx.Err(); err != nil {
			return StepResult{}, err
		}
		next.Version = current.Version + 1
		next.UpdatedAt = r.now().UTC()
		if err := r.stateStore.Save(ctx, &next); err != nil {
			return StepResult{}, fmt.Errorf("save state: %w", err)
		}
		r.currentObservation = cloneObservation(obs)
		return StepResult{Done: decision.Done, Decision: decision, Observation: cloneObservation(obs)}, nil
	}
	failure := func(next state.AgentState, decision agent.Decision, source, kind string, cause error) (StepResult, error) {
		if err := ctx.Err(); err != nil {
			return StepResult{}, err
		}
		if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
			return StepResult{}, cause
		}
		tool := ""
		if decision.ToolCall != nil {
			tool = decision.ToolCall.Name
		}
		obs := newObservation(source, tool, nil, &observation.ErrorInfo{Type: kind, Message: cause.Error()})
		result, err := finish(next, decision, obs)
		// An invalid Done decision is feedback, not a terminal result.
		result.Done = false
		return result, err
	}
	decision, err := r.agent.Decide(ctx, agent.StepInput{
		Goal: current.Goal, State: current.Clone(), Observation: cloneObservation(r.currentObservation), Tools: definitions,
	})
	if err != nil {
		var invalid *agent.InvalidDecisionError
		if errors.As(err, &invalid) {
			return failure(current.Clone(), decision, "validator", "invalid_decision", err)
		}
		return StepResult{}, fmt.Errorf("agent decision: %w", err)
	}
	if err := r.validators.Decision.Validate(ctx, decision); err != nil {
		return failure(current.Clone(), decision, "validator", "invalid_decision", err)
	}
	next := current.Clone()
	if decision.StatePatch != nil {
		if err := r.validators.StatePatch.Validate(ctx, current.Clone(), *decision.StatePatch); err != nil {
			return failure(current.Clone(), decision, "validator", "invalid_state_patch", err)
		}
		next, err = state.ApplyPatch(next, *decision.StatePatch)
		if err != nil {
			return failure(current.Clone(), decision, "validator", "invalid_state_patch", err)
		}
	}
	if decision.Done || decision.ToolCall == nil {
		return finish(next, decision, nil)
	}
	call := *decision.ToolCall
	def, exists := r.toolRegistry.Get(ctx, call.Name)
	if !exists {
		return failure(current.Clone(), decision, "validator", "unknown_tool", fmt.Errorf("unknown tool %q", call.Name))
	}
	if err := tools.ValidateInput(def, call.Arguments); err != nil {
		return failure(current.Clone(), decision, "validator", "invalid_tool_arguments", err)
	}
	if err := r.validators.Permission.Validate(ctx, next.Clone(), call); err != nil {
		return failure(current.Clone(), decision, "validator", "permission_denied", err)
	}
	if err := r.validators.Action.Validate(ctx, next.Clone(), call); err != nil {
		return failure(current.Clone(), decision, "validator", "action_rejected", err)
	}
	if err := ctx.Err(); err != nil {
		return StepResult{}, err
	}
	result, err := r.toolRegistry.Execute(ctx, call)
	if err != nil {
		var fatal *tools.FatalError
		if errors.As(err, &fatal) {
			return StepResult{}, fmt.Errorf("execute tool: %w", err)
		}
		return failure(next, decision, def.Source, "tool_execution_error", err)
	}
	if err := tools.ValidateSchema(def.OutputSchema, result.Data); err != nil {
		return failure(next, decision, "validator", "invalid_tool_output", err)
	}
	obs := newObservation(def.Source, call.Name, result.Data, nil)
	for _, reducer := range r.reducers {
		if reducer.Supports(*cloneObservation(obs)) {
			next, err = reducer.Reduce(next.Clone(), *cloneObservation(obs))
			if err != nil {
				return StepResult{}, fmt.Errorf("reduce observation: %w", err)
			}
		}
	}
	return finish(next, decision, obs)
}

func (r *Runtime) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := r.Step(ctx)
		if err != nil {
			return err
		}
		if result.Done {
			return nil
		}
	}
}
