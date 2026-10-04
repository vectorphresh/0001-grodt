package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/vectorphresh/0001-grodt/internal/loop"
	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/stateflow"
	"github.com/vectorphresh/0001-grodt/internal/structured"
)

// GoalEvaluation is an application judgment, never a state mutation.
type GoalEvaluation struct {
	Achieved  bool   `json:"achieved"`
	Rationale string `json:"rationale"`
}

const evaluationInstructions = "Determine whether the original objective has been achieved using the supplied execution information. Set achieved=true only when the objective is satisfied by the current response. An intermediate step is not completion. Return the requested structured evaluation with a concise outcome rationale, not private reasoning."
const evaluationSchema = `{"type":"object","properties":{"achieved":{"type":"boolean"},"rationale":{"type":"string"}},"required":["achieved","rationale"],"additionalProperties":false}`

func evaluate(ctx context.Context, client openai.Client, objective, initial string, state loop.State) (out GoalEvaluation, opErr error) {
	var accepted json.RawMessage
	defer func() {
		if err := stateflow.Observe(ctx, client, "goal_evaluation", accepted, opErr); err != nil {
			out = GoalEvaluation{}
			opErr = err
		}
	}()
	data, err := json.Marshal(struct {
		Objective       string   `json:"objective"`
		InitialPrompt   string   `json:"initial_prompt"`
		Context         []string `json:"context"`
		CurrentResponse string   `json:"current_response"`
	}{objective, initial, state.Context, state.Response})
	if err != nil {
		return GoalEvaluation{}, errors.New("cannot encode evaluation input")
	}
	spec := openai.JSONSpecification{Name: "goal_evaluation", Schema: json.RawMessage(evaluationSchema), Strict: true}
	result, err := structured.Generate(ctx, spec.Schema, func(ctx context.Context, feedback json.RawMessage) (openai.JSONResult, error) {
		return client.RequestMutation(ctx, structured.WithFeedback(evaluationInstructions, feedback), data, json.RawMessage(`{"purpose":"goal_evaluation"}`), spec)
	})
	if err != nil {
		return GoalEvaluation{}, err
	}
	evaluation, err := decodeEvaluation(result.JSON)
	if err == nil {
		accepted = result.JSON
	}
	return evaluation, err
}

// Decode exact keys, rejecting duplicates, nulls, omissions, and wrong types.
func decodeEvaluation(data []byte) (GoalEvaluation, error) {
	invalid := errors.New("invalid goal evaluation")
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return GoalEvaluation{}, invalid
	}
	seen := map[string]bool{}
	var wire struct {
		Achieved  *bool
		Rationale *string
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return GoalEvaluation{}, invalid
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return GoalEvaluation{}, invalid
		}
		seen[key] = true
		switch key {
		case "achieved":
			err = decoder.Decode(&wire.Achieved)
		case "rationale":
			err = decoder.Decode(&wire.Rationale)
		default:
			return GoalEvaluation{}, invalid
		}
		if err != nil {
			return GoalEvaluation{}, invalid
		}
	}
	if _, err := decoder.Token(); err != nil {
		return GoalEvaluation{}, invalid
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return GoalEvaluation{}, invalid
	}
	if wire.Achieved == nil || wire.Rationale == nil || strings.TrimSpace(*wire.Rationale) == "" {
		return GoalEvaluation{}, invalid
	}
	return GoalEvaluation{Achieved: *wire.Achieved, Rationale: *wire.Rationale}, nil
}

// completionEvaluator judges completion after a provider has finished its work.
// The default remains the existing LLM evaluation; scenario hosts may verify
// authoritative outcomes themselves. A negative evaluation always permits continuation.
type completionEvaluator func(context.Context, openai.Client, string, string, loop.State) (GoalEvaluation, error)

func runObjective(ctx context.Context, objective, initial string, providers []loop.Provider, client openai.Client, output io.Writer, status *terminalStatus, metrics *runMetrics, store *runstate.Store) error {
	return runObjectiveWithEvaluator(ctx, objective, initial, providers, client, output, status, metrics, store, evaluate)
}

// Only valid evaluations drive continuation. Temporary guidance remains separate
// from run state. Accepted observations and failure metadata survive failed cycles;
// failed cycles never publish a response or retry provider errors.
func runObjectiveWithEvaluator(ctx context.Context, objective, initial string, providers []loop.Provider, client openai.Client, output io.Writer, status *terminalStatus, metrics *runMetrics, store *runstate.Store, evaluator completionEvaluator) (runErr error) {
	if evaluator == nil {
		return errors.New("completion evaluator is required")
	}
	if strings.TrimSpace(objective) == "" {
		return errors.New("run objective must not be blank")
	}
	if strings.TrimSpace(initial) == "" {
		return errors.New("initial prompt must not be blank")
	}
	if _, err := store.Push(ctx, objective, initial); err != nil {
		return err
	}
	input, _ := json.Marshal(struct {
		Objective string `json:"objective"`
		Request   string `json:"request"`
	}{objective, initial})
	if _, err := store.Admit(ctx, runstate.Source{Kind: "user", ID: store.Snapshot().Intrinsic.RunID + "/user"}, input); err != nil {
		status := "failed"
		if errors.Is(err, context.Canceled) {
			status = "cancelled"
		}
		_ = store.End(context.WithoutCancel(ctx), status)
		return err
	}
	outcome := "failed"
	rationale := ""
	defer func() {
		if runErr != nil && outcome == "failed" {
			rationale = "Operation failed; cycle discarded."
			var toolErr *loop.ToolOperationError
			if errors.As(runErr, &toolErr) {
				rationale = toolErr.Error() + "; cycle discarded."
			}
		}
		if errors.Is(runErr, context.Canceled) {
			outcome = "cancelled"
			rationale = "Run cancelled."
		}
		if errors.Is(runErr, context.DeadlineExceeded) {
			rationale = "Operation timed out; cycle discarded."
		}
		if outcome != "achieved" {
			taskStatus := outcome
			if taskStatus != "cancelled" && taskStatus != "incomplete" {
				taskStatus = "failed"
			}
			_ = store.End(context.WithoutCancel(ctx), taskStatus)
		}
		if reportErr := reportRun(status, objective, outcome, rationale, *metrics); reportErr != nil {
			runErr = reportErr
		}
	}()
	history := []string{}
	for cycle := 1; cycle <= maxCycles; cycle++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := store.BeginCycle(ctx); err != nil {
			return err
		}
		if err := status.log("Cycle %d/%d", cycle, maxCycles); err != nil {
			return err
		}
		prompt := initial
		if cycle > 1 {
			prompt += "\nThis is a subsequent attempt. Address the previous evaluation guidance."
		}
		state := loop.State{Objective: objective, Prompt: prompt, Context: slices.Clone(history)}
		if err := loop.Process(ctx, &state, providers); err != nil {
			return safeCycleError(ctx, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		evaluation, err := evaluator(ctx, client, objective, initial, state)
		if err != nil {
			return safeCycleError(ctx, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rationale = evaluation.Rationale
		if err := status.log("Evaluation: achieved=%t; rationale: %s", evaluation.Achieved, rationale); err != nil {
			return err
		}
		if evaluation.Achieved || cycle == maxCycles {
			if state.Response != "" {
				if _, err := fmt.Fprintln(output, state.Response); err != nil {
					return errors.New("write response failed")
				}
			}
			if evaluation.Achieved {
				if err := store.Complete(ctx, state.Response); err != nil {
					return err
				}
				outcome = "achieved"
				return nil
			}
			outcome = "incomplete"
			return errIncomplete
		}
		// Replace temporary guidance. The store owns durable knowledge; responses
		// and evaluation feedback must not become an append-only prompt archive.
		history = nil
		if state.Response != "" {
			history = append(history, "Previous response:\n"+continuationText(state.Response))
		}
		history = append(history, "Evaluation rationale (temporary guidance):\n"+continuationText(rationale))
	}
	return errIncomplete
}

// Bound temporary prose even when a model echoes a whole state snapshot.
func continuationText(text string) string {
	const maxRunes = 4096
	runes := []rune(text)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "\n[Temporary guidance truncated; use current state for authoritative facts.]"
	}
	return text
}

func safeCycleError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded, runstate.ErrHostBudget} {
		if errors.Is(err, sentinel) {
			return sentinel
		}
	}
	var toolErr *loop.ToolOperationError
	if errors.As(err, &toolErr) {
		return fmt.Errorf("run failed; cycle discarded: %w", toolErr)
	}
	return errors.New("run failed; cycle discarded")
}
