package openai_test

import (
	"context"
	"encoding/json"
	llm "github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOptionalModelAcrossOperations(t *testing.T) {
	for _, model := range []string{"", " ", " custom-model "} {
		t.Run(model, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var fields map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
					t.Error(err)
				}
				value, present := fields["model"]
				if model == " custom-model " {
					if string(value) != `"custom-model"` {
						t.Errorf("model=%s", value)
					}
				} else if present {
					t.Error("blank model sent")
				}
				completion(w, `{}`)
			}))
			defer server.Close()
			c, err := llm.NewClient(llm.Config{BaseURL: server.URL + "/v1", APIKey: "test-key", Model: model})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if _, err = c.Prompt(ctx, "request"); err != nil {
				t.Fatal(err)
			}
			if _, err = c.PromptWithSpecification(ctx, "instructions", "request", json.RawMessage(`{}`), specification()); err != nil {
				t.Fatal(err)
			}
			if _, err = c.RequestMutation(ctx, "instructions", json.RawMessage(`{}`), json.RawMessage(`{}`), specification()); err != nil {
				t.Fatal(err)
			}
			if _, err = c.(toolcall.Client).GenerateWithTools(ctx, toolcall.Request{Messages: []toolcall.Message{{Role: "user", Text: "request"}}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
