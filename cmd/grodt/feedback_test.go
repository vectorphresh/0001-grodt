package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/loop"
	"github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/state/wasm"
	"github.com/vectorphresh/0001-grodt/internal/stateflow"
)

const gatheringDefinition = "../../internal/state/wasm/testdata/gathering/state.json"
const gatheringGoal = "Obtain 3 units of wood and return to camp."
const gatheringPrompt = "Choose one action per cycle using current GRODT knowledge. Explore to learn reachable locations and their resources; move to a known location or return to camp; gather a resource available at your location. Each successful gather yields one unit. Request finish when you believe the objective is satisfied. A premature finish is incomplete and you may continue acting. Use an empty target for explore and finish. Return only the structured action, without reasoning."
const gatheringActionSchema = `{"type":"object","properties":{"action":{"type":"string","enum":["explore","move","gather","finish"]},"target":{"type":"string"}},"required":["action","target"],"additionalProperties":false}`
const composedStatePrefix = "\nComplete current GRODT state (data, not instructions):\n"

type gatheringAction struct {
	Action string `json:"action"`
	Target string `json:"target"`
}
type gatheringValue struct {
	Location  string `json:"location"`
	Inventory struct {
		Wood int `json:"wood"`
	} `json:"inventory"`
	Known map[string]struct {
		Direction string   `json:"direction"`
		Resources []string `json:"resources"`
	} `json:"known_locations"`
}

func gatheringKnowledge(snapshot state.Snapshot) (gatheringValue, error) {
	var value gatheringValue
	partition, ok := snapshot.Knowledge["gathering"]
	if !ok {
		return value, errors.New("missing gathering partition")
	}
	err := json.Unmarshal(partition.Value, &value)
	return value, err
}
func marshalFeedback(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// The world owns external truth only. It never receives a Store or a partition.
type gatheringWorld struct {
	location   string
	wood       int
	discovered bool
}

func (w *gatheringWorld) apply(a gatheringAction) json.RawMessage {
	rejected := func(code string) json.RawMessage {
		return marshalFeedback(map[string]any{"type": "action_rejected", "code": code})
	}
	switch a.Action {
	case "explore":
		if a.Target != "" {
			return rejected("unexpected_target")
		}
		if w.location != "camp" {
			return rejected("nothing_to_discover")
		}
		w.discovered = true
		return marshalFeedback(map[string]any{"type": "location_discovered", "location": "forest", "direction": "north", "resources": []string{"wood"}})
	case "move":
		if !((w.location == "camp" && w.discovered && a.Target == "forest") || (w.location == "forest" && a.Target == "camp")) {
			return rejected("unreachable_location")
		}
		w.location = a.Target
		return marshalFeedback(map[string]any{"type": "location_changed", "location": w.location})
	case "gather":
		if w.location != "forest" || a.Target != "wood" {
			return rejected("unavailable_resource")
		}
		w.wood++
		return marshalFeedback(map[string]any{"type": "resource_gathered", "resource": "wood", "quantity": 1})
	case "finish":
		if a.Target != "" {
			return rejected("unexpected_target")
		}
		return marshalFeedback(map[string]any{"type": "completion_requested"})
	}
	return rejected("unknown_action")
}

type gatheringStep struct {
	Before, After state.Snapshot
	Action        gatheringAction
	Event         state.Event
}
type feedbackHarness struct {
	store       *state.Store
	world       gatheringWorld
	provider    loop.Provider
	inputs      []state.Snapshot
	steps       []gatheringStep
	evaluations []GoalEvaluation
	metrics     runMetrics
	diagnostics bytes.Buffer
	output      bytes.Buffer
	status      *terminalStatus
}

// Inspect the actual client-bound input, not a privileged Store read. The live
// and scripted clients both pass through this same evidence collector.
type feedbackInputClient struct {
	openai.Client
	harness *feedbackHarness
}

func (c feedbackInputClient) PromptWithSpecification(ctx context.Context, i, p string, input json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
	at := strings.LastIndex(i, composedStatePrefix)
	if at < 0 {
		return openai.JSONResult{}, errors.New("decision missing composed state")
	}
	var snapshot state.Snapshot
	if err := json.Unmarshal([]byte(i[at+len(composedStatePrefix):]), &snapshot); err != nil {
		return openai.JSONResult{}, errors.New("invalid composed state")
	}
	if len(c.harness.inputs) == 0 {
		entire := strings.ToLower(i + p + string(input) + string(spec.Schema))
		for _, secret := range []string{"forest", "north"} {
			if strings.Contains(entire, secret) {
				return openai.JSONResult{}, errors.New("initial decision leaked undiscovered world")
			}
		}
	}
	c.harness.inputs = append(c.harness.inputs, snapshot)
	result, err := c.Client.PromptWithSpecification(ctx, i, p, input, spec)
	// Log returned JSON before validation, including candidates that need correction.
	// This is diagnostic output only; admission remains the provider's responsibility.
	if len(result.JSON) > 0 {
		if logErr := c.harness.status.log("cycle=%d request=%d llm_response=%s", snapshot.Intrinsic.GlobalCycle, len(c.harness.inputs), result.JSON); logErr != nil {
			return openai.JSONResult{}, logErr
		}
	}
	return result, err
}

func newFeedbackHarness(t *testing.T) *feedbackHarness {
	t.Helper()
	store, err := wasm.Load(context.Background(), gatheringDefinition, state.Options{RunID: "gathering-run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	return &feedbackHarness{store: store, world: gatheringWorld{location: "camp"}}
}
func (h *feedbackHarness) run(ctx context.Context, client openai.Client, key string) error {
	h.status = &terminalStatus{writer: &h.diagnostics, key: key}
	observed := observedClient{Client: feedbackInputClient{Client: client, harness: h}, status: h.status, metrics: &h.metrics}
	withState := &stateflow.Client{Client: observed, Store: h.store}
	h.provider = loop.NewStructuredProvider(withState, "Select the next action toward the objective from the supplied current state.", openai.JSONSpecification{Name: "gathering_action", Schema: json.RawMessage(gatheringActionSchema), Strict: true})
	return runObjectiveWithEvaluator(ctx, gatheringGoal, gatheringPrompt, []loop.Provider{h}, withState, &h.output, h.status, &h.metrics, h.store, h.evaluate)
}
func (h *feedbackHarness) Handle(ctx context.Context, working *loop.State) (bool, error) {
	before := h.store.Snapshot()
	handled, err := h.provider.Handle(ctx, working)
	if err != nil || !handled {
		return handled, err
	}
	var action gatheringAction
	if err = json.Unmarshal([]byte(working.Response), &action); err != nil {
		return false, err
	}
	// Accepted LLM actions must have no authoritative effect on the partition.
	if !reflect.DeepEqual(before.Knowledge, h.store.Snapshot().Knowledge) {
		return false, errors.New("LLM action mutated domain state")
	}
	event, err := h.store.Admit(ctx, state.Source{Kind: "runtime", ID: "gathering-environment"}, h.world.apply(action))
	if err != nil {
		return false, err
	}
	after := h.store.Snapshot()
	h.steps = append(h.steps, gatheringStep{Before: before, After: after, Action: action, Event: event})
	value, err := gatheringKnowledge(after)
	if err != nil {
		return false, err
	}
	known := []string{}
	for location := range value.Known {
		known = append(known, location)
	}
	sort.Strings(known)
	var eventType struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(event.Payload, &eventType)
	err = h.status.log("cycle=%d task=%s action=%s target=%q event=%s event_id=%s version=%d location=%s wood=%d known_locations=%v", after.Intrinsic.GlobalCycle, event.TaskID, action.Action, action.Target, eventType.Type, event.ID, after.Knowledge["gathering"].Metadata.Version, value.Location, value.Inventory.Wood, known)
	return err == nil, err
}
func (h *feedbackHarness) evaluate(ctx context.Context, client openai.Client, _, _ string, _ loop.State) (GoalEvaluation, error) {
	snapshot := h.store.Snapshot()
	value, err := gatheringKnowledge(snapshot)
	if err != nil {
		return GoalEvaluation{}, err
	}
	a := h.steps[len(h.steps)-1].Action
	requested := a.Action == "finish" && a.Target == ""
	achieved := requested && !snapshot.Knowledge["gathering"].Metadata.Stale && value.Location == "camp" && value.Inventory.Wood >= 3 && h.world.location == "camp" && h.world.wood >= 3 && h.world.wood == value.Inventory.Wood
	evaluation := GoalEvaluation{Achieved: achieved, Rationale: "Objective incomplete. Choose the next action using current state."}
	if requested && !achieved {
		evaluation.Rationale = gatheringCompletionRejection(value, snapshot.Knowledge["gathering"].Metadata.Stale, h.world)
	}
	if achieved {
		evaluation.Rationale = "Completion request verified against authoritative state and environment truth."
	}
	h.evaluations = append(h.evaluations, evaluation)
	if err = stateflow.Observe(ctx, client, "goal_evaluation", marshalFeedback(evaluation), nil); err != nil {
		return GoalEvaluation{}, err
	}
	return evaluation, nil
}

// Explain failed predicates using recorded knowledge, without revealing world
// values that have not entered state or choosing the agent's next action.
func gatheringCompletionRejection(value gatheringValue, stale bool, world gatheringWorld) string {
	reasons := []string{"Completion rejected."}
	if value.Inventory.Wood < 3 {
		unit := "units"
		if value.Inventory.Wood == 1 {
			unit = "unit"
		}
		reasons = append(reasons, fmt.Sprintf("Recorded inventory contains %d %s of wood; the objective requires at least 3.", value.Inventory.Wood, unit))
	} else if value.Location != "camp" {
		reasons = append(reasons, fmt.Sprintf("Recorded inventory contains %d units of wood, satisfying the requirement of at least 3.", value.Inventory.Wood))
	}
	if value.Location != "camp" {
		reasons = append(reasons, fmt.Sprintf("The recorded location is %q; the objective requires the current location to be %q.", value.Location, "camp"))
	}
	if stale {
		reasons = append(reasons, "The gathering partition is stale, so its recorded values cannot establish completion.")
	}
	if world.location != value.Location || world.wood != value.Inventory.Wood {
		reasons = append(reasons, "Recorded state and independent environment verification disagree; completion cannot be verified.")
	}
	return strings.Join(reasons, " ")
}

// This fake chooses from the exact state delivered to the inference boundary.
// It has no world reference and does not store a prewritten sequence.
type feedbackDecisionClient struct {
	openai.Client
	decide func(state.Snapshot) (json.RawMessage, error)
}

func (c feedbackDecisionClient) PromptWithSpecification(_ context.Context, i, _ string, _ json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
	at := strings.LastIndex(i, composedStatePrefix)
	if at < 0 {
		return openai.JSONResult{}, errors.New("missing state")
	}
	var snapshot state.Snapshot
	if err := json.Unmarshal([]byte(i[at+len(composedStatePrefix):]), &snapshot); err != nil {
		return openai.JSONResult{}, err
	}
	response, err := c.decide(snapshot)
	return openai.JSONResult{JSON: response}, err
}
func chooseGathering(snapshot state.Snapshot) (json.RawMessage, error) {
	value, err := gatheringKnowledge(snapshot)
	if err != nil {
		return nil, err
	}
	if value.Inventory.Wood >= 3 {
		if value.Location == "camp" {
			return marshalFeedback(gatheringAction{Action: "finish"}), nil
		}
		return marshalFeedback(gatheringAction{Action: "move", Target: "camp"}), nil
	}
	if info, ok := value.Known[value.Location]; ok {
		for _, resource := range info.Resources {
			if resource == "wood" {
				return marshalFeedback(gatheringAction{Action: "gather", Target: resource}), nil
			}
		}
	}
	locations := []string{}
	for name := range value.Known {
		locations = append(locations, name)
	}
	sort.Strings(locations)
	for _, name := range locations {
		for _, resource := range value.Known[name].Resources {
			if resource == "wood" {
				return marshalFeedback(gatheringAction{Action: "move", Target: name}), nil
			}
		}
	}
	return marshalFeedback(gatheringAction{Action: "explore"}), nil
}

func assertFeedbackMilestone(t *testing.T, h *feedbackHarness) {
	t.Helper()
	if len(h.inputs) == 0 || len(h.steps) == 0 {
		t.Fatal("missing causal evidence")
	}
	initial, err := gatheringKnowledge(h.inputs[0])
	if err != nil {
		t.Fatal(err)
	}
	if initial.Location != "camp" || initial.Inventory.Wood != 0 || len(initial.Known) != 0 {
		t.Fatal("invalid initial knowledge")
	}
	discovered := uint64(0)
	movedAfterDiscovery := false
	gathers := 0
	for _, step := range h.steps {
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(step.Event.Payload, &event) != nil {
			t.Fatal("invalid event")
		}
		partition := step.After.Knowledge["gathering"]
		if step.Event.Source.Kind != "runtime" || step.Event.Source.ID != "gathering-environment" || step.Event.TaskID != "gathering-run/task/1" {
			t.Fatal("invalid event provenance/task")
		}
		switch event.Type {
		case "location_discovered":
			after, _ := gatheringKnowledge(step.After)
			info, ok := after.Known["forest"]
			if !ok || info.Direction != "north" || !reflect.DeepEqual(info.Resources, []string{"wood"}) || partition.Metadata.LastUpdateSequence != step.Event.Sequence {
				t.Fatal("discovery not committed")
			}
			if discovered == 0 {
				discovered = step.Event.Sequence
			}
		case "location_changed":
			if step.Action.Target == "forest" {
				before, _ := gatheringKnowledge(step.Before)
				if discovered == 0 || before.Known["forest"].Direction != "north" {
					t.Fatal("movement preceded knowledge")
				}
				seen := false
				for _, input := range h.inputs {
					v, _ := gatheringKnowledge(input)
					if input.Intrinsic.GlobalCycle == step.Before.Intrinsic.GlobalCycle && input.Knowledge["gathering"].Metadata.LastUpdateSequence >= discovered && v.Known["forest"].Direction == "north" {
						seen = true
					}
				}
				if !seen {
					t.Fatal("moving decision did not receive discovery")
				}
				movedAfterDiscovery = true
			}
		case "resource_gathered":
			before, _ := gatheringKnowledge(step.Before)
			after, _ := gatheringKnowledge(step.After)
			if after.Inventory.Wood != before.Inventory.Wood+1 || partition.Metadata.LastUpdateSequence != step.Event.Sequence {
				t.Fatal("gather not committed through event")
			}
			gathers++
		}
	}
	snapshot := h.store.Snapshot()
	final, _ := gatheringKnowledge(snapshot)
	if !movedAfterDiscovery || gathers < 3 || final.Location != "camp" || final.Inventory.Wood < 3 || h.world.location != final.Location || h.world.wood != final.Inventory.Wood || h.steps[len(h.steps)-1].Action.Action != "finish" {
		t.Fatal("milestone goal/evidence absent")
	}
	if snapshot.Knowledge["gathering"].Metadata.Stale || snapshot.Intrinsic.HostExecutions != 0 || snapshot.Intrinsic.GlobalCycle != uint64(len(h.steps)) || len(snapshot.Tasks.Records) != 1 || len(snapshot.Tasks.Stack) != 0 || snapshot.Tasks.Records["gathering-run/task/1"].Status != "completed" {
		t.Fatal("invalid run/task lifecycle")
	}
	events := map[string]state.Event{}
	for i, entry := range h.store.Journal() {
		if entry.Sequence != uint64(i+1) {
			t.Fatal("journal ordering")
		}
		if entry.Kind == "event" {
			var e state.Event
			if json.Unmarshal(entry.Data, &e) != nil {
				t.Fatal("event journal")
			}
			events[e.ID] = e
		}
		if entry.Kind == "mutation" {
			e, ok := events[entry.EventID]
			if !ok || e.Source.Kind != "runtime" || e.Source.ID != "gathering-environment" {
				t.Fatal("mutation lacks prior environment event")
			}
		}
	}
	if !h.evaluations[len(h.evaluations)-1].Achieved {
		t.Fatal("independent evaluator did not verify goal")
	}
}

func TestStatefulFeedbackLoop(t *testing.T) {
	h := newFeedbackHarness(t)
	if err := h.run(context.Background(), feedbackDecisionClient{decide: chooseGathering}, ""); err != nil {
		t.Fatalf("%v\n%s", err, h.diagnostics.String())
	}
	assertFeedbackMilestone(t, h)
	if h.metrics.Requests != uint64(len(h.inputs)) {
		t.Fatal("structured request accounting missing")
	}
}
func TestPrematureFinishContinues(t *testing.T) {
	h := newFeedbackHarness(t)
	client := feedbackDecisionClient{decide: func(snapshot state.Snapshot) (json.RawMessage, error) {
		if snapshot.Intrinsic.GlobalCycle == 1 {
			return marshalFeedback(gatheringAction{Action: "finish"}), nil
		}
		return chooseGathering(snapshot)
	}}
	if err := h.run(context.Background(), client, ""); err != nil {
		t.Fatalf("%v\n%s", err, h.diagnostics.String())
	}
	if h.evaluations[0].Achieved || len(h.steps) < 2 || h.steps[0].After.Tasks.Records["gathering-run/task/1"].Status != "running" {
		t.Fatal("premature finish terminated run")
	}
	assertFeedbackMilestone(t, h)
}
func TestRepeatedPrematureFinishReachesCycleLimit(t *testing.T) {
	h := newFeedbackHarness(t)
	err := h.run(context.Background(), feedbackDecisionClient{decide: func(state.Snapshot) (json.RawMessage, error) {
		return marshalFeedback(gatheringAction{Action: "finish"}), nil
	}}, "")
	if !errors.Is(err, errIncomplete) || len(h.steps) != maxCycles || h.store.Snapshot().Tasks.Records["gathering-run/task/1"].Status != "incomplete" {
		t.Fatalf("premature finish bypassed limit: %v", err)
	}
	for _, evaluation := range h.evaluations {
		if evaluation.Achieved {
			t.Fatal("false completion")
		}
	}
}
func TestGatheringDecisionRequiresKnowledge(t *testing.T) {
	h := newFeedbackHarness(t)
	before := h.store.Snapshot()
	action, err := chooseGathering(before)
	if err != nil {
		t.Fatal(err)
	}
	if string(action) != `{"action":"explore","target":""}` {
		t.Fatal("fake knows undiscovered world")
	}
	if _, err = h.store.Admit(context.Background(), state.Source{Kind: "runtime", ID: "gathering-environment"}, h.world.apply(gatheringAction{Action: "explore"})); err != nil {
		t.Fatal(err)
	}
	action, err = chooseGathering(h.store.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if string(action) != `{"action":"move","target":"forest"}` {
		t.Fatal("fake ignores discovered knowledge")
	}
	// Re-presenting the original state still yields exploration.
	action, _ = chooseGathering(before)
	if string(action) != `{"action":"explore","target":""}` {
		t.Fatal("fake memorized action sequence")
	}
}
func TestGatheringModuleAdmissionAndValidation(t *testing.T) {
	h := newFeedbackHarness(t)
	for _, source := range []state.Source{{Kind: "llm", ID: "gathering-environment"}, {Kind: "runtime", ID: "unrelated"}} {
		if _, err := h.store.Admit(context.Background(), source, json.RawMessage(`{"type":"resource_gathered","resource":"wood","quantity":3}`)); err != nil {
			t.Fatal(err)
		}
	}
	if p := h.store.Snapshot().Knowledge["gathering"]; p.Metadata.Version != 0 || p.Metadata.LastProcessedSequence != 0 {
		t.Fatal("unrelated event changed partition")
	}
	// The real module proposes a candidate; the partition schema rejects its type.
	if _, err := h.store.Admit(context.Background(), state.Source{Kind: "runtime", ID: "gathering-environment"}, json.RawMessage(`{"type":"location_changed","location":42}`)); err != nil {
		t.Fatal(err)
	}
	p := h.store.Snapshot().Knowledge["gathering"]
	value, _ := gatheringKnowledge(h.store.Snapshot())
	if !p.Metadata.Stale || p.Metadata.Version != 0 || p.Metadata.LastError.Kind != "mutation_rejected" || value.Location != "camp" {
		t.Fatal("invalid candidate committed")
	}
}
func TestInvalidGatheringActionsAreObserved(t *testing.T) {
	h := newFeedbackHarness(t)
	client := feedbackDecisionClient{decide: func(snapshot state.Snapshot) (json.RawMessage, error) {
		if snapshot.Intrinsic.GlobalCycle == 1 {
			return marshalFeedback(gatheringAction{Action: "gather", Target: "wood"}), nil
		}
		return chooseGathering(snapshot)
	}}
	if err := h.run(context.Background(), client, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(h.steps[0].Event.Payload), `"type":"action_rejected"`) || !reflect.DeepEqual(h.steps[0].Before.Knowledge, h.steps[0].After.Knowledge) {
		t.Fatal("invalid action silently changed state")
	}
	assertFeedbackMilestone(t, h)
}

func TestFeedbackCorrectionDoesNotExecuteCandidate(t *testing.T) {
	h := newFeedbackHarness(t)
	calls := 0
	client := feedbackDecisionClient{decide: func(snapshot state.Snapshot) (json.RawMessage, error) {
		calls++
		if calls == 1 {
			return json.RawMessage(`{"action":"gather","target":"wood","unexpected":true}`), nil
		}
		return chooseGathering(snapshot)
	}}
	if err := h.run(context.Background(), client, ""); err != nil {
		t.Fatal(err)
	}
	assertFeedbackMilestone(t, h)
	if len(h.inputs) != len(h.steps)+1 || h.steps[0].Action.Action != "explore" || h.steps[0].Before.Intrinsic.GlobalCycle != 1 {
		t.Fatal("correction escaped structured operation boundary")
	}
	for _, entry := range h.store.Journal() {
		if entry.Kind == "event" && strings.Contains(string(entry.Data), "unexpected") {
			t.Fatal("rejected candidate admitted")
		}
	}
}
