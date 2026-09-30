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
	"github.com/vectorphresh/0001-grodt/internal/structured"
)

// GoalEvaluation is an application judgment, never a state mutation.
type GoalEvaluation struct {
	Achieved  bool   `json:"achieved"`
	Rationale string `json:"rationale"`
}

const evaluationInstructions = "Determine whether the original objective has been achieved using the supplied execution information. Set achieved=true only when the objective is satisfied by the current response. An intermediate step is not completion. Return the requested structured evaluation with a concise outcome rationale, not private reasoning."
const evaluationSchema = `{"type":"object","properties":{"achieved":{"type":"boolean"},"rationale":{"type":"string"}},"required":["achieved","rationale"],"additionalProperties":false}`

func evaluate(ctx context.Context, client openai.Client, objective, initial string, state loop.State) (GoalEvaluation, error) {
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
	return decodeEvaluation(result.JSON)
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

// Only valid evaluations drive continuation. Context is temporary model guidance,
// not authoritative world state. No failed cycle is committed or retried.
func runObjective(ctx context.Context, objective, initial string, providers []loop.Provider, client openai.Client, output io.Writer, status *terminalStatus, metrics *runMetrics) (runErr error) {
	if strings.TrimSpace(objective) == "" {
		return errors.New("run objective must not be blank")
	}
	if strings.TrimSpace(initial) == "" {
		return errors.New("initial prompt must not be blank")
	}
	outcome := "failed"
	rationale := ""
	defer func() {
		if runErr != nil && outcome == "failed" {
			rationale = "Operation failed; cycle discarded."
		}
		if errors.Is(runErr, context.Canceled) {
			outcome = "cancelled"
			rationale = "Run cancelled."
		}
		if errors.Is(runErr, context.DeadlineExceeded) {
			rationale = "Operation timed out; cycle discarded."
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
		evaluation, err := evaluate(ctx, client, objective, initial, state)
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
				outcome = "achieved"
				return nil
			}
			outcome = "incomplete"
			return errIncomplete
		}
		// Preserve relevant provider context only after successful generation/evaluation.
		history = slices.Clone(state.Context)
		if state.Response != "" {
			history = append(history, "Previous response:\n"+state.Response)
		}
		history = append(history, "Evaluation rationale (temporary guidance):\n"+rationale)
	}
	return errIncomplete
}

func safeCycleError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, sentinel) {
			return sentinel
		}
	}
	return errors.New("run failed; cycle discarded")
}
