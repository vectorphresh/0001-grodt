package contracts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/structured"
)

type fakeClient struct {
	selectFn func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error)
	reduceFn func(context.Context, string, json.RawMessage, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error)
}

func (fakeClient) Prompt(context.Context, string) (openai.TextResult, error) {
	panic("ordinary Prompt must not be used")
}
func (f fakeClient) PromptWithSpecification(c context.Context, i, p string, x json.RawMessage, s openai.JSONSpecification) (openai.JSONResult, error) {
	if f.selectFn == nil {
		panic("unexpected selection")
	}
	return f.selectFn(c, i, p, x, s)
}
func (f fakeClient) RequestMutation(c context.Context, i string, x, o json.RawMessage, s openai.JSONSpecification) (openai.JSONResult, error) {
	if f.reduceFn == nil {
		panic("unexpected reduction")
	}
	return f.reduceFn(c, i, x, o, s)
}
func definition() Definition {
	return Definition{Contracts: []Contract{
		{Name: "objective", Description: "Stable run goal", Schema: json.RawMessage(`{"type":"string"}`)},
		{Name: "account", Description: "Reported balances", Schema: json.RawMessage(`{"type":"object","properties":{"cash":{"type":"integer","minimum":9007199254740993}},"required":["cash"]}`), ModelWritable: true},
		{Name: "positions", Description: "Reported open instruments", Schema: json.RawMessage(`{"type":"array","items":{"type":"string"}}`), ModelWritable: true},
		{Name: "memory", Description: "Useful durable notes", Schema: json.RawMessage(`true`), ModelWritable: true},
	}}
}
func newReducer(t *testing.T, c openai.Client) *Reducer {
	t.Helper()
	r, err := New(c, definition())
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func validInput() Input {
	return Input{State: json.RawMessage(`{"account":{"cash":10}}`), Information: json.RawMessage(`{"cash":20}`)}
}
func asObject(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		t.Fatal("expected object")
	}
	return object
}

func TestDefinitionPreconditions(t *testing.T) {
	if _, err := New(nil, definition()); err == nil {
		t.Fatal("nil client accepted")
	}
	for _, tc := range []struct {
		name   string
		change func(*Definition)
	}{
		{"empty", func(d *Definition) { d.Contracts = nil }},
		{"duplicate", func(d *Definition) { d.Contracts = append(d.Contracts, d.Contracts[0]) }},
		{"blank name", func(d *Definition) { d.Contracts[0].Name = "" }},
		{"invalid name", func(d *Definition) { d.Contracts[0].Name = "has spaces" }},
		{"long name", func(d *Definition) { d.Contracts[0].Name = strings.Repeat("x", 65) }},
		{"blank description", func(d *Definition) { d.Contracts[0].Description = " \n" }},
		{"bad syntax", func(d *Definition) { d.Contracts[0].Schema = json.RawMessage(`{`) }},
		{"missing schema", func(d *Definition) { d.Contracts[0].Schema = nil }},
		{"null schema", func(d *Definition) { d.Contracts[0].Schema = json.RawMessage(`null`) }},
		{"array schema", func(d *Definition) { d.Contracts[0].Schema = json.RawMessage(`[]`) }},
		{"number schema", func(d *Definition) { d.Contracts[0].Schema = json.RawMessage(`1`) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := definition()
			tc.change(&d)
			if _, err := New(fakeClient{}, d); err == nil {
				t.Fatal("invalid definition accepted")
			}
		})
	}
	// Mechanical ingestion deliberately makes no judgment about schema meaning.
	for _, schema := range []string{`true`, `false`, `{}`, `{"unknown-keyword":"opaque"}`, `{"$ref":"#/caller-owned"}`} {
		d := definition()
		d.Contracts[0].Schema = json.RawMessage(schema)
		if _, err := New(fakeClient{}, d); err != nil {
			t.Fatal("schema semantics were inspected")
		}
	}
}

func TestDefinitionAndSpecificationsAreIndependentCopies(t *testing.T) {
	d := definition()
	r, err := New(fakeClient{}, d)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := r.PartialSpecification([]string{"account"})
	d.Contracts[1].Schema[0] = '!'
	d.Contracts[1].Name = "changed"
	d.Contracts[1].ModelWritable = false
	after, err := r.PartialSpecification([]string{"account"})
	if err != nil || !bytes.Equal(before.Schema, after.Schema) {
		t.Fatal("caller changed prepared definition")
	}
	after.Schema[0] = '!'
	again, _ := r.PartialSpecification([]string{"account"})
	if !bytes.Equal(before.Schema, again.Schema) {
		t.Fatal("returned schema aliases definition")
	}
}

func TestPartialSpecificationIsOptionalAndWritableOnly(t *testing.T) {
	r := newReducer(t, fakeClient{})
	spec, err := r.PartialSpecification([]string{"positions", "objective", "account"})
	if err != nil {
		t.Fatal(err)
	}
	root := asObject(t, spec.Schema)
	props := asObject(t, root["properties"])
	if len(props) != 2 || props["account"] == nil || props["positions"] == nil || string(root["additionalProperties"]) != "false" {
		t.Fatal("wrong subset")
	}
	if _, exists := root["required"]; exists {
		t.Fatal("selected contracts must remain optional")
	}
	if !bytes.Contains(props["account"], []byte(`9007199254740993`)) || !bytes.Contains(props["account"], []byte(`"required":["cash"]`)) {
		t.Fatal("fragment meaning changed")
	}
	for _, names := range [][]string{nil, {}, {"objective"}} {
		s, e := r.PartialSpecification(names)
		if e != nil || len(asObject(t, asObject(t, s.Schema)["properties"])) != 0 {
			t.Fatal("empty writable subset expanded")
		}
	}
	for _, names := range [][]string{{"unknown"}, {"account", "account"}} {
		if _, e := r.PartialSpecification(names); e == nil {
			t.Fatal("invalid subset accepted")
		}
	}
}

func TestSelectionTranslationAndIndependentUsage(t *testing.T) {
	usage := &openai.Usage{PromptTokens: 11, CompletionTokens: 4, TotalTokens: 15}
	for _, response := range []string{`{"relevant":[]}`, `{"relevant":["account"]}`, `{"relevant":["positions","account"]}`, `{"relevant":["objective"]}`} {
		calls := 0
		r := newReducer(t, fakeClient{selectFn: func(ctx context.Context, instructions, prompt string, data json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
			calls++
			if instructions == "" || prompt == "" || !spec.Strict {
				t.Error("selection instructions/spec missing")
			}
			root := asObject(t, data)
			if string(root["information"]) != `"plain text"` || string(root["context"]) != "null" {
				t.Error("opaque information/context changed")
			}
			var descriptors []map[string]json.RawMessage
			json.Unmarshal(root["contracts"], &descriptors)
			if len(descriptors) != 4 {
				t.Error("descriptors missing")
			}
			for _, d := range descriptors {
				if len(d) != 3 || d["name"] == nil || d["description"] == nil || d["model_writable"] == nil || d["schema"] != nil {
					t.Error("wrong descriptor surface")
				}
			}
			schema := asObject(t, spec.Schema)
			relevant := asObject(t, asObject(t, schema["properties"])["relevant"])
			items := asObject(t, relevant["items"])
			if string(items["enum"]) != `["objective","account","positions","memory"]` || string(relevant["uniqueItems"]) != "true" || string(schema["required"]) != `["relevant"]` || string(schema["additionalProperties"]) != "false" {
				t.Error("selection schema not exact")
			}
			return openai.JSONResult{JSON: json.RawMessage(response), Usage: usage}, nil
		}})
		result, err := r.Select(context.Background(), json.RawMessage(`"plain text"`), nil)
		if err != nil || calls != 1 || result.Requests != 1 || result.Usage != usage {
			t.Fatal("wrong selection accounting")
		}
		want := []string{}
		switch response {
		case `{"relevant":["account"]}`:
			want = []string{"account"}
		case `{"relevant":["positions","account"]}`:
			want = []string{"account", "positions"}
		case `{"relevant":["objective"]}`:
			want = []string{"objective"}
		}
		if !reflect.DeepEqual(result.Names, want) {
			t.Fatalf("selection = %v; want %v in definition order", result.Names, want)
		}
	}
}

func TestInvalidSelectionDoesNotBecomeEmptySuccess(t *testing.T) {
	for _, response := range []string{`null`, `{}`, `{"relevant":null}`, `{"relevant":[null]}`, `{"relevant":"account"}`, `{"relevant":[1]}`, `{"relevant":["unknown"]}`, `{"relevant":["account","account"]}`, `{"relevant":[],"other":0}`, `{"relevant":[],"relevant":[]}`, `{"relevant":null,"relevant":[]}`, `{"Relevant":[]}`, `{"relevant":[]} {}`, `bad JSON`} {
		r := newReducer(t, fakeClient{selectFn: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
			return openai.JSONResult{JSON: json.RawMessage(response)}, nil
		}})
		result, err := r.Select(context.Background(), json.RawMessage(`{}`), nil)
		want := uint64(1)
		if json.Valid([]byte(response)) {
			want = structured.MaxStructuredAttempts
		}
		if err == nil || result.Names != nil || result.Requests != want {
			t.Fatal("invalid selection accepted or accounting lost")
		}
	}
}

func TestReductionDoesNotRequireSelectionOrInterpretProposal(t *testing.T) {
	// A structurally valid proposal remains unchanged and is never applied.
	const proposal = " \n{\"account\":{\"cash\":9007199254740993}}\t"
	input := validInput()
	input.Context = json.RawMessage(`{"intent":"opaque"}`)
	stateBefore, infoBefore, contextBefore := string(input.State), string(input.Information), string(input.Context)
	calls := 0
	r := newReducer(t, fakeClient{reduceFn: func(_ context.Context, instructions string, state, observation json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		if instructions == "" || string(state) != stateBefore {
			t.Error("state/instructions not propagated")
		}
		obs := asObject(t, observation)
		if string(obs["information"]) != infoBefore || string(obs["context"]) != contextBefore {
			t.Error("information/context not separate")
		}
		props := asObject(t, asObject(t, spec.Schema)["properties"])
		if len(props) != 1 || props["account"] == nil {
			t.Error("wrong mutation subset")
		}
		return openai.JSONResult{JSON: json.RawMessage(proposal)}, nil
	}})
	for i := 0; i < 2; i++ {
		result, err := r.Reduce(context.Background(), input, []string{"account"})
		if err != nil || string(result.JSON) != proposal || result.Requests != 1 || result.Usage != nil {
			t.Fatal("proposal was interpreted or modified")
		}
	}
	if calls != 2 || string(input.State) != stateBefore || string(input.Information) != infoBefore || string(input.Context) != contextBefore {
		t.Fatal("retained sequencing state or changed input")
	}
}

func TestEmptySelectionSkipsMutation(t *testing.T) {
	r := newReducer(t, fakeClient{selectFn: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		return openai.JSONResult{JSON: json.RawMessage(`{"relevant":[]}`)}, nil
	}})
	selection, err := r.Select(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, names := range [][]string{selection.Names, {"objective"}} {
		mutation, err := r.Reduce(context.Background(), validInput(), names)
		if err != nil || string(mutation.JSON) != "{}" || mutation.Requests != 0 || mutation.Usage != nil {
			t.Fatal("empty writable subset made a request")
		}
	}
}

func TestReductionRejectsInvalidProposals(t *testing.T) {
	for _, response := range []string{`null`, `[]`, `1`, `"text"`, `{`, `{} {}`} {
		r := newReducer(t, fakeClient{reduceFn: func(context.Context, string, json.RawMessage, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
			return openai.JSONResult{JSON: json.RawMessage(response), Usage: &openai.Usage{TotalTokens: 7}}, nil
		}})
		result, err := r.Reduce(context.Background(), validInput(), []string{"memory"})
		want := uint64(1)
		if json.Valid([]byte(response)) {
			want = structured.MaxStructuredAttempts
		}
		if err == nil || result.JSON != nil || result.Requests != want || result.Usage == nil || result.Usage.TotalTokens != int64(want)*7 {
			t.Fatal("unusable proposal accepted or accounting lost")
		}
	}
}

func TestInputPreconditionsMakeNoRequests(t *testing.T) {
	r := newReducer(t, fakeClient{})
	if _, err := r.Select(context.Background(), nil, nil); err == nil {
		t.Fatal("missing information accepted")
	}
	if _, err := r.Select(context.Background(), json.RawMessage(`{}`), json.RawMessage(` `)); err == nil {
		t.Fatal("bad semantic context accepted")
	}
	for _, field := range []string{"state", "information", "context"} {
		input := validInput()
		switch field {
		case "state":
			input.State = nil
		case "information":
			input.Information = nil
		case "context":
			input.Context = json.RawMessage(`{`)
		}
		result, err := r.Reduce(context.Background(), input, nil)
		if err == nil || result.Requests != 0 {
			t.Fatal("invalid input accepted")
		}
	}
	for _, names := range [][]string{{"unknown"}, {"account", "account"}} {
		if _, err := r.Reduce(context.Background(), validInput(), names); err == nil {
			t.Fatal("bad subset accepted")
		}
	}
}

func TestErrorsPreserveContextAndAccounting(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errors.New("private diagnostic value")} {
		u := &openai.Usage{TotalTokens: 8}
		r := newReducer(t, fakeClient{
			selectFn: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
				return openai.JSONResult{Usage: u}, cause
			},
			reduceFn: func(context.Context, string, json.RawMessage, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
				return openai.JSONResult{Usage: u}, cause
			},
		})
		s, se := r.Select(context.Background(), json.RawMessage(`{}`), nil)
		m, me := r.Reduce(context.Background(), validInput(), []string{"account"})
		if s.Requests != 1 || m.Requests != 1 || s.Usage != u || m.Usage != u || s.Names != nil || m.JSON != nil {
			t.Fatal("failure accounting lost")
		}
		for _, err := range []error{se, me} {
			if err == nil {
				t.Fatal("failure swallowed")
			}
			if cause == context.Canceled || cause == context.DeadlineExceeded {
				if !errors.Is(err, cause) {
					t.Fatal("context identity lost")
				}
			} else if strings.Contains(err.Error(), cause.Error()) {
				t.Fatal("unsafe diagnostic exposed")
			}
		}
	}
	r := newReducer(t, fakeClient{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, err := r.Select(ctx, json.RawMessage(`{}`), nil)
	if !errors.Is(err, context.Canceled) || s.Requests != 0 {
		t.Fatal("pre-canceled selection invoked")
	}
	m, err := r.Reduce(ctx, validInput(), nil)
	if !errors.Is(err, context.Canceled) || m.Requests != 0 {
		t.Fatal("pre-canceled empty reduction succeeded")
	}
}

func TestConcurrentDefinitionReuse(t *testing.T) {
	r := newReducer(t, fakeClient{
		selectFn: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
			return openai.JSONResult{JSON: json.RawMessage(`{"relevant":["account"]}`)}, nil
		},
		reduceFn: func(context.Context, string, json.RawMessage, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
			return openai.JSONResult{JSON: json.RawMessage(`{}`)}, nil
		},
	})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Select(context.Background(), json.RawMessage(`{}`), nil); err != nil {
				t.Error(err)
			}
			if _, err := r.Reduce(context.Background(), validInput(), []string{"positions"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
