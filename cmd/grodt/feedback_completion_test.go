package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/loop"
	"github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/stateflow"
)

func TestGatheringCompletionReasonsAndAuthority(t *testing.T) {
	const staleReason = "The gathering partition is stale, so its recorded values cannot establish completion."
	const inconsistentReason = "Recorded state and independent environment verification disagree; completion cannot be verified."
	for _, tc := range []struct {
		name     string
		wood     int
		location string
		fault    string
		achieved bool
		want     string
	}{
		{"insufficient_wood", 2, "camp", "", false, "Completion rejected. Recorded inventory contains 2 units of wood; the objective requires at least 3."},
		{"incorrect_location", 3, "forest", "", false, `Completion rejected. Recorded inventory contains 3 units of wood, satisfying the requirement of at least 3. The recorded location is "forest"; the objective requires the current location to be "camp".`},
		{"both_conditions", 1, "forest", "", false, `Completion rejected. Recorded inventory contains 1 unit of wood; the objective requires at least 3. The recorded location is "forest"; the objective requires the current location to be "camp".`},
		{"stale", 3, "camp", "stale", false, "Completion rejected. " + staleReason},
		{"environment_location", 3, "camp", "location", false, "Completion rejected. " + inconsistentReason},
		{"environment_inventory", 3, "camp", "inventory", false, "Completion rejected. " + inconsistentReason},
		{"undiscovered_environment", 0, "camp", "undiscovered", false, "Completion rejected. Recorded inventory contains 0 units of wood; the objective requires at least 3. " + inconsistentReason},
		{"satisfied", 3, "camp", "", true, "Completion request verified against authoritative state and environment truth."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFeedbackHarness(t)
			ctx := context.Background()
			if _, err := h.store.Push(ctx, gatheringGoal, gatheringPrompt); err != nil {
				t.Fatal(err)
			}
			admitAction := func(a gatheringAction) {
				t.Helper()
				if _, err := h.store.Admit(ctx, state.Source{Kind: "runtime", ID: "gathering-environment"}, h.world.apply(a)); err != nil {
					t.Fatal(err)
				}
			}
			if tc.fault == "undiscovered" {
				// Lost observations change external truth without exposing it to the partition.
				h.world.apply(gatheringAction{Action: "explore"})
				h.world.apply(gatheringAction{Action: "move", Target: "forest"})
			} else {
				admitAction(gatheringAction{Action: "explore"})
				admitAction(gatheringAction{Action: "move", Target: "forest"})
				for range tc.wood {
					admitAction(gatheringAction{Action: "gather", Target: "wood"})
				}
				if tc.fault == "inventory" {
					h.world.apply(gatheringAction{Action: "gather", Target: "wood"})
				}
				if tc.location == "camp" {
					admitAction(gatheringAction{Action: "move", Target: "camp"})
				}
				if tc.fault == "location" {
					h.world.apply(gatheringAction{Action: "move", Target: "forest"})
				}
			}
			if tc.fault == "stale" {
				if _, err := h.store.Admit(ctx, state.Source{Kind: "runtime", ID: "gathering-environment"}, json.RawMessage(`{"type":"location_changed","location":42}`)); err != nil {
					t.Fatal(err)
				}
			}
			before := h.store.Snapshot().Knowledge
			finish := gatheringAction{Action: "finish"}
			admitAction(finish)
			h.steps = append(h.steps, gatheringStep{Action: finish})
			client := &stateflow.Client{Store: h.store}
			got, err := h.evaluate(ctx, client, gatheringGoal, gatheringPrompt, loop.State{})
			if err != nil {
				t.Fatal(err)
			}
			if got.Achieved != tc.achieved || got.Rationale != tc.want {
				t.Fatalf("evaluation: %+v; want achieved=%v rationale=%q", got, tc.achieved, tc.want)
			}
			if !reflect.DeepEqual(before, h.store.Snapshot().Knowledge) {
				t.Fatal("finish or evaluation changed authoritative partition")
			}
			for _, instruction := range []string{"call ", "move(", "gather(", "explore", "next action", "tool", "north"} {
				if strings.Contains(strings.ToLower(got.Rationale), instruction) {
					t.Fatalf("feedback prescribes action or leaks world: %s", got.Rationale)
				}
			}
			if tc.fault == "undiscovered" && strings.Contains(got.Rationale, "forest") {
				t.Fatal("environment verification disclosed undiscovered location")
			}
		})
	}
}

type completionInputClient struct {
	openai.Client
	inspect func(json.RawMessage)
}

func (c completionInputClient) PromptWithSpecification(ctx context.Context, i, p string, input json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
	c.inspect(input)
	return c.Client.PromptWithSpecification(ctx, i, p, input, spec)
}

func TestGatheringRejectionReachesNextInference(t *testing.T) {
	for _, tc := range []struct {
		name, location string
		wood           int
	}{
		{"insufficient_wood", "camp", 0},
		{"incorrect_location", "forest", 3},
		{"both_conditions", "forest", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFeedbackHarness(t)
			rejected, checked := false, false
			var expected string
			var beforeFinish map[string]state.Partition
			raw := feedbackDecisionClient{decide: func(snapshot state.Snapshot) (json.RawMessage, error) {
				value, err := gatheringKnowledge(snapshot)
				if err != nil {
					return nil, err
				}
				if !rejected && value.Location == tc.location && value.Inventory.Wood == tc.wood {
					rejected = true
					beforeFinish = snapshot.Knowledge
					return marshalFeedback(gatheringAction{Action: "finish"}), nil
				}
				return chooseGathering(snapshot)
			}}
			client := completionInputClient{Client: raw, inspect: func(input json.RawMessage) {
				if !rejected || checked {
					return
				}
				if len(h.evaluations) == 0 {
					t.Fatal("decision occurred before evaluation")
				}
				evaluation := h.evaluations[len(h.evaluations)-1]
				if evaluation.Achieved || !strings.HasPrefix(evaluation.Rationale, "Completion rejected.") {
					t.Fatal("missing rejected completion evaluation")
				}
				expected = evaluation.Rationale
				var request struct {
					Context []string `json:"context"`
				}
				if err := json.Unmarshal(input, &request); err != nil {
					t.Fatal(err)
				}
				want := "Evaluation rationale (temporary guidance):\n" + expected
				if len(request.Context) == 0 || request.Context[len(request.Context)-1] != want {
					t.Fatalf("exact rationale absent from subsequent structured input: %s", input)
				}
				if !reflect.DeepEqual(beforeFinish, h.store.Snapshot().Knowledge) {
					t.Fatal("rejected finish changed partition before subsequent decision")
				}
				checked = true
			}}
			if err := h.run(context.Background(), client, ""); err != nil {
				t.Fatalf("%v\n%s", err, h.diagnostics.String())
			}
			if !checked || expected == "" {
				t.Fatal("feedback delivery not exercised")
			}
			assertFeedbackMilestone(t, h)
		})
	}
}
