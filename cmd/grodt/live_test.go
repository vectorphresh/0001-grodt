//go:build live

package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/openai"
)

// Explicitly opt in: go test -tags live ./cmd/grodt -run '^TestLive' -v -count=1
func TestLiveAutonomousGoals(t *testing.T) {
	base, key := os.Getenv("OPENAI_BASE_URL"), os.Getenv("OPENAI_API_KEY")
	if base == "" || strings.TrimSpace(key) == "" {
		t.Fatal("live endpoint and credential are required")
	}
	client, err := openai.NewClient(openai.Config{BaseURL: base, APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, objective, prompt string
		requests                string
	}{
		{"OneCycle", defaultObjective, "Return the exact token GRODT-7319 and nothing else.", "Requests: 2\n"},
		{"MultiCycle", "Produce the final answer GRODT-7319 exactly. An intermediate DRAFT response does not achieve the objective.", "On your first attempt, respond with exactly DRAFT. On subsequent attempts, provide the final answer requested by the objective.", "Requests: 4\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			var out, diag bytes.Buffer
			if err := execute(ctx, tc.objective, tc.prompt, client, &out, &terminalStatus{writer: &diag, key: key, trace: true}); err != nil {
				t.Fatal(err)
			}
			if out.String() != "GRODT-7319\n" || !strings.Contains(diag.String(), "Status: achieved") || !strings.Contains(diag.String(), tc.requests) {
				t.Fatal("unexpected live completion")
			}
			coverage := "Usage coverage: 2/2 requests"
			if tc.name == "MultiCycle" {
				coverage = "Usage coverage: 4/4 requests"
				for _, part := range []string{"Evaluation: achieved=false", "Previous response:\nDRAFT", "Evaluation rationale (temporary guidance):"} {
					if !strings.Contains(diag.String(), part) {
						t.Fatal("continuation evidence absent")
					}
				}
			}
			if !strings.Contains(diag.String(), coverage) || strings.Contains(diag.String(), "unavailable") {
				t.Fatal("live usage unavailable")
			}
			t.Logf("Response: %s", strings.TrimSpace(out.String()))
			// Report only the final summary, avoiding unnecessary prompt/context output.
			index := strings.LastIndex(diag.String(), "\nObjective:")
			if index < 0 {
				t.Fatal("missing summary")
			}
			t.Log(diag.String()[index+1:])
		})
	}
}
