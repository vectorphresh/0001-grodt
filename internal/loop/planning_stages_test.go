package loop

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

func TestPlanningStagesBracketValidationAndEvaluation(t *testing.T) {
	for _, mode := range []string{"accepted", "rejected", "cancelled", "writer_failure"} {
		t.Run(mode, func(t *testing.T) {
			p, fixture := toolsHarness(t, []runstate.Definition{{Name: "configuration", Schema: json.RawMessage(`true`), Initial: json.RawMessage(`{"setting":"enabled"}`), Module: &eventModule{}}}, false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var cursor uint64
			var stages []string
			p.FlushMilestones = func() error {
				entries, next := p.Store.JournalSince(cursor, "planning_stage")
				cursor = next
				for _, entry := range entries {
					var stage struct {
						Status string `json:"status"`
						Reason string `json:"reason"`
					}
					if err := json.Unmarshal(entry.Data, &stage); err != nil {
						t.Fatal(err)
					}
					stages = append(stages, stage.Status)
					if stage.Status == "rejected" && stage.Reason == "" {
						t.Fatal("rejection lacks reason")
					}
				}
				if mode == "writer_failure" {
					return errors.New("diagnostic writer failed")
				}
				return nil
			}
			evaluated := false
			p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
				evaluated = true
				if !reflect.DeepEqual(stages, []string{"started"}) {
					t.Fatal("start not flushed before evaluation", stages)
				}
				if mode == "cancelled" {
					cancel()
					return openai.JSONResult{}, ctx.Err()
				}
				return openai.JSONResult{JSON: json.RawMessage(`{"satisfied":true,"rationale":"Accepted setting established."}`)}, nil
			}}
			turns := 0
			p.Client = nativeClient(func(context.Context, toolcall.Request) (toolcall.Response, error) {
				turns++
				if turns > 1 && mode != "rejected" {
					return toolcall.Response{Text: "Finished."}, nil
				}
				plan := runstate.Plan{Description: "Inspect", Status: "active", Steps: []runstate.Step{{ID: "inspect", Description: "Inspect setting", Status: "satisfied", Outcome: "Setting enabled", Evidence: []runstate.EvidenceReference{{Partition: "configuration", Version: 0, Path: "/setting"}}}, {ID: "follow_up", Description: "Follow up", Status: "pending"}}}
				if mode == "rejected" {
					if turns == 1 {
						plan.Steps[0].Evidence = nil
					} else if turns == 2 {
						plan.Steps[0].Status = "active"
						plan.Steps[0].Outcome = ""
						plan.Steps[0].Evidence = nil
					} else {
						return toolcall.Response{Text: "Corrected."}, nil
					}
				}
				return planCall(t, "plan", planCommand{Action: "revise", Plan: &plan}), nil
			})
			_, err := p.Handle(ctx, &State{Objective: "Inspect"})
			if mode == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if mode == "writer_failure" && (err == nil || evaluated || turns != 1) {
				t.Fatal("processing continued after writer failure", err, turns)
			}
			if (mode == "accepted" || mode == "rejected") && err != nil {
				t.Fatal(err)
			}
			want := []string{"started", "accepted"}
			if mode != "accepted" && mode != "rejected" {
				want[1] = "rejected"
			}
			// Invalid initial proposals are rejected before managePlan begins.
			if !reflect.DeepEqual(stages, want) {
				t.Fatal("unpaired planning stages", stages)
			}
			active, _ := p.Store.Active()
			if (active.Plan != nil) != (mode == "accepted" || mode == "rejected") || fixture.Calls.Load() != 0 {
				t.Fatal("unexpected acceptance or external dispatch")
			}
		})
	}
}
