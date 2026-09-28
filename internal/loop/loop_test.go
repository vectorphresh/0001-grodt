package loop

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/vectorphresh/0001-grodt/internal/openai"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

type providerFunc func(context.Context, *State) (bool, error)

func (f providerFunc) Handle(ctx context.Context, s *State) (bool, error) { return f(ctx, s) }

func TestProviderFallthrough(t *testing.T) {
	var order []int
	state := State{Prompt: "hello"}
	err := Process(context.Background(), &state, []Provider{
		providerFunc(func(_ context.Context, s *State) (bool, error) {
			order = append(order, 1)
			s.Context = append(s.Context, "contribution")
			return false, nil
		}),
		providerFunc(func(_ context.Context, s *State) (bool, error) {
			order = append(order, 2)
			if !reflect.DeepEqual(s.Context, []string{"contribution"}) {
				t.Error("context missing")
			}
			s.Response = "done"
			return true, nil
		}),
		providerFunc(func(context.Context, *State) (bool, error) {
			t.Error("traversal continued after handling")
			return true, nil
		}),
	})
	if err != nil || !reflect.DeepEqual(order, []int{1, 2}) || state.Response != "done" {
		t.Fatalf("unexpected traversal: %v", err)
	}
}

func TestStopAfterHandle(t *testing.T) {
	err := Process(context.Background(), &State{}, []Provider{
		providerFunc(func(context.Context, *State) (bool, error) { return true, nil }),
		providerFunc(func(context.Context, *State) (bool, error) { t.Error("second provider invoked"); return true, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProviderErrorsStopTraversalWithoutRetry(t *testing.T) {
	for _, cause := range []error{errors.New("operation failed"), context.DeadlineExceeded, context.Canceled} {
		calls := 0
		err := Process(context.Background(), &State{}, []Provider{
			providerFunc(func(_ context.Context, s *State) (bool, error) { calls++; s.Response = "partial"; return true, cause }),
			providerFunc(func(context.Context, *State) (bool, error) { t.Error("error treated as fallthrough"); return true, nil }),
		})
		if !errors.Is(err, cause) || calls != 1 {
			t.Fatal("error identity lost or request retried")
		}
	}
}

func TestUnhandled(t *testing.T) {
	for _, providers := range [][]Provider{nil, {providerFunc(func(context.Context, *State) (bool, error) { return false, nil })}} {
		if err := Process(context.Background(), &State{}, providers); !errors.Is(err, ErrUnhandled) {
			t.Fatal("expected ErrUnhandled")
		}
	}
}

func TestBlockingProviderPreventsAdvancement(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var second atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- Process(context.Background(), &State{}, []Provider{
			providerFunc(func(context.Context, *State) (bool, error) { close(entered); <-release; return false, nil }),
			providerFunc(func(context.Context, *State) (bool, error) { second.Store(true); return true, nil }),
		})
	}()
	<-entered
	if second.Load() {
		t.Error("overlapping provider operation")
	}
	select {
	case <-done:
		t.Error("Process returned while operation active")
	default:
	}
	close(release)
	if err := <-done; err != nil || !second.Load() {
		t.Fatal("traversal failed after operation completed")
	}
}

func TestDeadlineAbortsBlockingProvider(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := Process(ctx, &State{}, []Provider{
		providerFunc(func(ctx context.Context, _ *State) (bool, error) { <-ctx.Done(); return false, ctx.Err() }),
		providerFunc(func(context.Context, *State) (bool, error) { t.Error("advanced after timeout"); return true, nil }),
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("deadline identity lost")
	}
}

func TestCancellationStopsTraversal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	err := Process(ctx, &State{}, []Provider{
		providerFunc(func(context.Context, *State) (bool, error) { cancel(); return false, nil }),
		providerFunc(func(context.Context, *State) (bool, error) { t.Error("advanced after cancellation"); return true, nil }),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("expected cancellation")
	}
	if err := Process(ctx, &State{}, []Provider{providerFunc(func(context.Context, *State) (bool, error) {
		t.Error("invoked with canceled context")
		return true, nil
	})}); !errors.Is(err, context.Canceled) {
		t.Fatal("expected cancellation")
	}
}

type promptClient struct {
	prompt func(context.Context, string) (string, error)
}

func (c promptClient) Prompt(ctx context.Context, p string) (openai.TextResult, error) {
	text, err := c.prompt(ctx, p)
	return openai.TextResult{Text: text}, err
}
func (promptClient) PromptWithSpecification(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
	panic("unused")
}

func (promptClient) RequestMutation(context.Context, string, json.RawMessage, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
	panic("unused")
}

func TestGenericProviderPromptAggregation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		objective string
		context   []string
		want      string
	}{
		{"no context", "", nil, " exact prompt "},
		{"objective only", "Stable goal", nil, "Objective:\nStable goal\n\nPrompt:\n exact prompt "},
		{"objective with context", "Stable goal", []string{"first", "second"}, "Objective:\nStable goal\n\nContext:\nfirst\n\nsecond\n\nPrompt:\n exact prompt "},
		{"context", "", []string{"first", "second"}, "Context:\nfirst\n\nsecond\n\nPrompt:\n exact prompt "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), struct{}{}, "marker")
			calls := 0
			p := NewGenericProvider(promptClient{func(gotCtx context.Context, prompt string) (string, error) {
				calls++
				if gotCtx != ctx || prompt != tc.want {
					t.Error("prompt or context changed")
				}
				return "answer", nil
			}})
			state := State{Objective: tc.objective, Prompt: " exact prompt ", Context: tc.context}
			handled, err := p.Handle(ctx, &state)
			if err != nil || !handled || state.Response != "answer" || calls != 1 {
				t.Fatal("unexpected generic result")
			}
		})
	}
}

func TestGenericProviderDoesNotPublishFailedResponse(t *testing.T) {
	cause := context.DeadlineExceeded
	p := NewGenericProvider(promptClient{func(context.Context, string) (string, error) { return "partial", cause }})
	state := State{Prompt: "hello"}
	handled, err := p.Handle(context.Background(), &state)
	if handled || !errors.Is(err, cause) || state.Response != "" {
		t.Fatal("failed response published")
	}
}

func TestGenericProviderRejectsBlankPrompt(t *testing.T) {
	p := NewGenericProvider(promptClient{func(context.Context, string) (string, error) { t.Error("blank prompt sent"); return "", nil }})
	if handled, err := p.Handle(context.Background(), &State{Prompt: " ", Context: []string{"context"}}); handled || err == nil {
		t.Fatal("blank prompt accepted")
	}
}
