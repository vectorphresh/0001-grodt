//go:build live

package openai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	llm "github.com/vectorphresh/0001-grodt/internal/openai"
)

// Run explicitly with: go test -tags live ./internal/openai -run '^TestLive' -v
// Only this test harness reads environment configuration; the package does not.
func TestLiveLlamaCPP(t *testing.T) {
	base, key := os.Getenv("OPENAI_BASE_URL"), os.Getenv("OPENAI_API_KEY")
	if base == "" || strings.TrimSpace(key) == "" {
		t.Fatal("live tests require OPENAI_BASE_URL and OPENAI_API_KEY")
	}
	c, err := llm.NewClient(llm.Config{BaseURL: base, APIKey: key, Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("Prompt", func(t *testing.T) {
		text, err := c.Prompt(context.Background(), "Respond with exactly: GRODT_OK")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("Response: %q", text.Text)
		if strings.TrimSpace(text.Text) != "GRODT_OK" {
			t.Fatal("unexpected vanilla response")
		}
	})
	t.Run("PromptWithSpecification", func(t *testing.T) {
		result, err := c.PromptWithSpecification(context.Background(), "Read the supplied JSON context and follow the requested output specification.", "Return the cash value.", json.RawMessage(`{"world":{"cash":12345}}`), llm.JSONSpecification{
			Name: "cash_response", Description: "Return the cash value from the supplied world state.", Strict: true,
			Schema: json.RawMessage(`{"type":"object","properties":{"cash":{"type":"integer"}},"required":["cash"],"additionalProperties":false}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		assertLiveJSON(t, result.JSON, `{"cash":12345}`)
	})
	spec := llm.JSONSpecification{Name: "state_mutation", Strict: true, Schema: json.RawMessage(`{"type":"object","properties":{"upsert_memory":{"type":"array","items":{"type":"object","properties":{"key":{"type":"string"},"value":{"type":"string"}},"required":["key","value"],"additionalProperties":false}}},"required":["upsert_memory"],"additionalProperties":false}`)}
	state := json.RawMessage(`{"working_memory":[{"key":"market","value":"closed"}]}`)
	original := string(state)
	for _, tc := range []struct {
		name, instructions string
		observation        json.RawMessage
	}{
		{"RequestMutationWithoutObservation", "Read the current state and propose only the mutation permitted by the response specification. Change market from closed to open.", nil},
		{"RequestMutationWithObservation", "Use the current state and latest observation to propose the next legal mutation.", json.RawMessage(`{"event":"market_opened"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := c.RequestMutation(context.Background(), tc.instructions, state, tc.observation, spec)
			if err != nil {
				t.Fatal(err)
			}
			assertLiveJSON(t, result.JSON, `{"upsert_memory":[{"key":"market","value":"open"}]}`)
			if string(state) != original {
				t.Fatal("caller state was modified")
			}
		})
	}
}

func assertLiveJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, got, "", "  "); err != nil {
		t.Fatalf("invalid JSON response: %q", got)
	}
	t.Logf("JSON response:\n%s", formatted.String())
	var a, b any
	if json.Unmarshal(got, &a) != nil || json.Unmarshal([]byte(want), &b) != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("structured result did not match expected semantics")
	}
}
