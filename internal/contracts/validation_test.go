package contracts

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/validation"
)

func TestStructuredCorrections(t *testing.T) {
	selections, reductions := 0, 0
	client := fakeClient{
		selectFn: func(_ context.Context, instructions, prompt string, input json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
			selections++
			candidate := `{"relevant":["unknown"]}`
			if selections == 2 {
				if !strings.Contains(instructions, `"candidate":`) || !strings.Contains(instructions, `"validation":{`) || !strings.Contains(instructions, "unknown") {
					t.Fatal("missing selection feedback")
				}
				candidate = `{"relevant":["memory"]}`
			}
			return openai.JSONResult{JSON: json.RawMessage(candidate), Usage: &openai.Usage{TotalTokens: 5}}, nil
		},
		reduceFn: func(_ context.Context, instructions string, state, observation json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
			reductions++
			candidate := `{"memory":42}`
			if reductions == 2 {
				if !strings.Contains(instructions, `"validation":{`) || string(state) != string(validInput().State) {
					t.Fatal("lost feedback or original state")
				}
				candidate = " \n{\"memory\":{\"note\":\"UTC\"}}\t"
			}
			return openai.JSONResult{JSON: json.RawMessage(candidate), Usage: &openai.Usage{TotalTokens: 7}}, nil
		},
	}
	d := definition()
	d.Contracts[3].Schema = json.RawMessage(`{"type":"object","properties":{"note":{"type":"string"}},"required":["note"],"additionalProperties":false}`)
	r, err := New(client, d)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := r.Select(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil || selections != 2 || selected.Requests != 2 || selected.UsageRequests != 2 || selected.Usage.TotalTokens != 10 || len(selected.Names) != 1 || selected.Names[0] != "memory" {
		t.Fatalf("%+v %v", selected, err)
	}
	mutation, err := r.Reduce(context.Background(), validInput(), selected.Names)
	if err != nil || reductions != 2 || mutation.Requests != 2 || mutation.UsageRequests != 2 || mutation.Usage.TotalTokens != 14 || string(mutation.JSON) != " \n{\"memory\":{\"note\":\"UTC\"}}\t" {
		t.Fatalf("%+v %v", mutation, err)
	}
}

func TestReductionSchemaFailure(t *testing.T) {
	calls := 0
	d := definition()
	d.Contracts[1].Schema = json.RawMessage(`{"type":"unknown"}`)
	r, err := New(fakeClient{reduceFn: func(context.Context, string, json.RawMessage, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		return openai.JSONResult{JSON: json.RawMessage(`{}`)}, nil
	}}, d)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Reduce(context.Background(), validInput(), []string{"account"})
	var schemaErr *validation.SchemaError
	if !errors.As(err, &schemaErr) || calls != 1 || result.JSON != nil {
		t.Fatalf("%+v %v", result, err)
	}
}
