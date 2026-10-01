package stateflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/contracts"
	"github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/stateflow"
)

type module struct{ events []state.Event }

func (m *module) Process(_ context.Context, _ json.RawMessage, e state.Event) (json.RawMessage, error) {
	m.events = append(m.events, e)
	return json.RawMessage(`{"status":"processed"}`), nil
}
func (m *module) Close(context.Context) error { return nil }

type client struct {
	calls     int
	responses []string
	err       error
	t         *testing.T
}

func (c *client) check(i string) {
	c.t.Helper()
	if !strings.Contains(i, `"run_id":"test"`) || !strings.Contains(i, `"knowledge"`) || !strings.Contains(i, `"tasks"`) {
		c.t.Fatal("complete state absent")
	}
}
func (c *client) Prompt(_ context.Context, p string) (openai.TextResult, error) {
	c.check(p)
	return openai.TextResult{Text: "text"}, c.err
}
func (c *client) PromptWithSpecification(_ context.Context, i, p string, input json.RawMessage, s openai.JSONSpecification) (openai.JSONResult, error) {
	c.check(i)
	return c.next()
}
func (c *client) RequestMutation(_ context.Context, i string, s, o json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
	c.check(i)
	return c.next()
}
func (c *client) next() (openai.JSONResult, error) {
	index := c.calls
	c.calls++
	if index >= len(c.responses) {
		index = len(c.responses) - 1
	}
	return openai.JSONResult{JSON: json.RawMessage(c.responses[index]), Usage: &openai.Usage{TotalTokens: 5}}, c.err
}
func setup(t *testing.T, raw *client) (*state.Store, *module, *stateflow.Client) {
	t.Helper()
	m := &module{}
	s, err := state.New(context.Background(), []state.Definition{{Name: "sample", Schema: json.RawMessage(`true`), Initial: json.RawMessage(`{}`), Module: m}}, state.Options{RunID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	return s, m, &stateflow.Client{Client: raw, Store: s}
}
func TestAdmissionAfterCorrections(t *testing.T) {
	for _, tt := range []struct {
		name      string
		responses []string
		calls     int
		accepted  bool
	}{
		{"corrected", []string{`{"sample":42}`, `{"sample":"ok"}`}, 2, true},
		{"exhausted", []string{`{"sample":42}`}, 3, false},
		{"malformed", []string{`{`}, 1, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := &client{t: t, responses: tt.responses}
			s, m, c := setup(t, raw)
			r, err := contracts.New(c, contracts.Definition{Contracts: []contracts.Contract{{Name: "sample", Description: "test", Schema: json.RawMessage(`{"type":"string"}`), ModelWritable: true}}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := r.Reduce(context.Background(), contracts.Input{State: json.RawMessage(`{}`), Information: json.RawMessage(`{}`)}, []string{"sample"})
			if raw.calls != tt.calls || (err == nil) != tt.accepted || result.Requests != uint64(tt.calls) || result.Usage.TotalTokens != int64(tt.calls)*5 {
				t.Fatalf("%+v %v calls=%d", result, err, raw.calls)
			}
			if len(m.events) != 1 || s.Snapshot().Intrinsic.EventSequence != 1 {
				t.Fatal("intermediate candidates were delivered")
			}
			var payload struct {
				Status string
				Result json.RawMessage
				Error  string
			}
			json.Unmarshal(m.events[0].Payload, &payload)
			if tt.accepted {
				if payload.Status != "accepted" || string(payload.Result) != `{"sample":"ok"}` {
					t.Fatal("wrong accepted result")
				}
			} else if payload.Status != "error" || payload.Result != nil || payload.Error != "operation_failed" {
				t.Fatal("invalid candidate in terminal event")
			}
		})
	}
}
func TestUnstructuredAndSelectionAdmission(t *testing.T) {
	raw := &client{t: t, responses: []string{`{"relevant":["sample"]}`}}
	_, m, c := setup(t, raw)
	if _, err := c.Prompt(context.Background(), "request"); err != nil {
		t.Fatal(err)
	}
	r, err := contracts.New(c, contracts.Definition{Contracts: []contracts.Contract{{Name: "sample", Description: "test", Schema: json.RawMessage(`true`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Select(context.Background(), json.RawMessage(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	if len(m.events) != 2 {
		t.Fatal("accepted boundaries missing")
	}
}
func TestRawStructuredResponseDoesNotEmit(t *testing.T) {
	raw := &client{t: t, responses: []string{`{}`}}
	_, m, c := setup(t, raw)
	c.RequestMutation(context.Background(), "instructions", json.RawMessage(`{}`), nil, openai.JSONSpecification{})
	c.PromptWithSpecification(context.Background(), "instructions", "prompt", json.RawMessage(`{}`), openai.JSONSpecification{})
	if len(m.events) != 0 {
		t.Fatal("raw client response admitted")
	}
}
func TestErrorsSanitizedAndCancellationDoesNotDispatch(t *testing.T) {
	raw := &client{t: t, err: errors.New("secret-token")}
	s, m, c := setup(t, raw)
	c.Prompt(context.Background(), "test")
	if len(m.events) != 1 || strings.Contains(string(m.events[0].Payload), "secret-token") {
		t.Fatal("unsafe error observation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := stateflow.Observe(ctx, c, "test", nil, ctx.Err()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(m.events) != 1 || len(s.Journal()) < 3 {
		t.Fatal("cancellation event dispatched or diagnostic missing")
	}
}
