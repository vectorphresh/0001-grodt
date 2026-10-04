package openai_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

func TestNativeToolsWireAndContinuation(t *testing.T) {
	requests := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid wire JSON")
		}
		if _, ok := req["model"]; ok {
			t.Error("unexpected model")
		}
		if _, ok := req["response_format"]; ok {
			t.Error("structured contract leaked into native calls")
		}
		var tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name       string          `json:"name"`
				Parameters json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		if json.Unmarshal(req["tools"], &tools) != nil || len(tools) != 1 || tools[0].Function.Name != "alias" {
			t.Error("missing native tool")
		}
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"checking","tool_calls":[{"id":"native-1","type":"function","function":{"name":"alias","arguments":"{\"n\":9007199254740993}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}}`))
			return
		}
		var messages []struct {
			Role  string            `json:"role"`
			ID    string            `json:"tool_call_id"`
			Calls []json.RawMessage `json:"tool_calls"`
		}
		if json.Unmarshal(req["messages"], &messages) != nil || len(messages) != 4 || len(messages[2].Calls) != 1 || messages[3].ID != "native-1" {
			t.Error("lost native tool correlation")
		}
		completion(w, "done")
	}, time.Second)
	native := client.(toolcall.Client)
	request := toolcall.Request{Instructions: "objective and state", Tools: []toolcall.Definition{{Name: "alias", InputSchema: json.RawMessage(`{"type":"object"}`)}}, Messages: []toolcall.Message{{Role: "user", Text: "query"}}}
	result, err := native.GenerateWithTools(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Calls) != 1 || string(result.Calls[0].Arguments) != `{"n":9007199254740993}` || result.TotalTokens != 14 {
		t.Fatal(result)
	}
	request.Messages = append(request.Messages, toolcall.Message{Role: "assistant", Text: result.Text, Calls: result.Calls}, toolcall.Message{Role: "tool", CallID: "native-1", Text: `{"content":[]}`})
	result, err = native.GenerateWithTools(context.Background(), request)
	if err != nil || result.Text != "done" {
		t.Fatal(result, err)
	}
}
