package loop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

func TestRecoverableFailureFeedback(t *testing.T) {
	for _, mode := range []string{"markup", "schema", "provider"} {
		t.Run(mode, func(t *testing.T) {
			p, f := toolsHarness(t, nil, false)
			requests := 0
			p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
				requests++
				if requests == 1 {
					switch mode {
					case "markup":
						return toolcall.Response{Text: "<function=alpaca.get_account_info></function>"}, nil
					case "provider":
						return toolcall.Response{}, &openai.ProviderRequestError{StatusCode: 400}
					case "schema":
						c := toolRequest(r, "invalid")
						c.Arguments = []byte(`{"key":123}`)
						return toolcall.Response{Calls: []toolcall.Call{c}}, nil
					}
				}
				if requests == 2 {
					if f.Calls.Load() != 0 || len(p.Store.Snapshot().Tasks.Records) != 1 {
						t.Fatal("failed request dispatched work")
					}
					if !strings.Contains(r.Messages[len(r.Messages)-1].Text, "executed") {
						t.Fatal("failure feedback missing")
					}
					return toolcall.Response{Calls: []toolcall.Call{toolRequest(r, "corrected")}}, nil
				}
				if len(r.Messages) != 3 || r.Messages[2].CallID != "corrected" {
					t.Fatal("correction polluted native conversation")
				}
				return toolcall.Response{Text: "done"}, nil
			})
			s := &State{Objective: "goal", Prompt: "request"}
			if _, err := p.Handle(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			if requests != 3 || f.Calls.Load() != 1 || s.Response != "done" {
				t.Fatal("correction failed")
			}
		})
	}
}

func TestFailureFeedbackBoundedAndAuthenticationTerminal(t *testing.T) {
	for _, status := range []int{400, 401, 403} {
		p, f := toolsHarness(t, nil, false)
		requests := 0
		cause := &openai.ProviderRequestError{StatusCode: status}
		p.Client = nativeClient(func(context.Context, toolcall.Request) (toolcall.Response, error) {
			requests++
			return toolcall.Response{}, cause
		})
		_, err := p.Handle(context.Background(), &State{Objective: "goal"})
		want := 1
		if status == 400 {
			want = 3
		}
		if !errors.Is(err, cause) || requests != want || f.Calls.Load() != 0 {
			t.Fatal("unbounded correction or terminal failure retried")
		}
	}
}
