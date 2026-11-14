package loop

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

func TestActorContextOverflowRepairsWithoutDispatch(t *testing.T) {
	p, fixture := toolsExecutionHarness(t, []runstate.Definition{{Name: "large", Schema: json.RawMessage(`{"type":"object"}`), Initial: mustEncode(map[string]string{"csv": strings.Repeat("12345,67890\n", 20000)}), Module: &eventModule{}}}, false)
	before := p.Store.Snapshot()
	calls, originalBytes := 0, 0
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		calls++
		if calls == 1 {
			originalBytes = len(r.Instructions)
		}
		if calls > 1 {
			if len(r.Instructions) >= originalBytes/2 || !strings.Contains(r.Instructions, "knowledge_projection") {
				t.Fatal("oversized context not reduced")
			}
			active, _ := p.Store.Active()
			if !reflect.DeepEqual(active.Plan, before.Tasks.Records[active.ID].Plan) || fixture.Calls.Load() != 0 {
				t.Fatal("repair changed plan or dispatched failed request")
			}
		}
		if calls <= 4 {
			return toolcall.Response{}, &openai.ProviderRequestError{StatusCode: 400, Response: json.RawMessage(`{"code":400,"type":"exceed_context_size_error","message":"request exceeds context"}`)}
		}
		return toolcall.Response{Text: "Continue from preserved state"}, nil
	})
	if _, err := p.Handle(context.Background(), &State{Objective: "Inspect"}); err != nil {
		t.Fatal(err)
	}
	if calls != 5 || fixture.Calls.Load() != 0 {
		t.Fatal("context repair stopped at the old two-correction limit")
	}
	if !strings.Contains(string(mustEncode(p.Store.Journal())), "model_context_repair") {
		t.Fatal("repair not audited")
	}
}

func TestContextRepairKeepsNativePairingAndOriginalHistory(t *testing.T) {
	history := []toolcall.Message{{Role: "user", Text: "objective"}, {Role: "assistant", Calls: []toolcall.Call{{ID: "completed", Name: "query", Arguments: json.RawMessage(`{}`)}}}, {Role: "tool", CallID: "completed", Text: strings.Repeat("large payload", 10000)}}
	original := mustEncode(history)
	repaired := compactActorMessages(cloneMessages(history), 32<<10)
	if repaired[2].CallID != repaired[1].Calls[0].ID || len(repaired[2].Text) > 4096 || !strings.Contains(repaired[2].Text, "payload_omitted") {
		t.Fatal("lost pairing or presented a partial payload as complete")
	}
	if string(mustEncode(history)) != string(original) {
		t.Fatal("repair mutated retained exchange")
	}
}

func TestContextRepairRecognizesOnlyContextMetadata(t *testing.T) {
	for _, body := range []string{`{"message":"context_length_exceeded"}`, `{"type":"authentication_error"}`} {
		if modelContextExceeded(&openai.ProviderRequestError{StatusCode: 400, Response: json.RawMessage(body)}) {
			t.Fatal("provider prose treated as repair authority")
		}
	}
	if !modelContextExceeded(&openai.ProviderRequestError{StatusCode: 400, Response: json.RawMessage(`{"error":{"code":"context_length_exceeded"}}`)}) {
		t.Fatal("standard context error not recognized")
	}
}
