package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConfiguredReasoningEffort(t *testing.T) {
	for _, tc := range []struct {
		name, yamlEffort, envEffort, wantOnWire, wantErr string
	}{
		{name: "absent", wantOnWire: ""},
		{name: "yaml none", yamlEffort: "none", wantOnWire: "none"},
		{name: "environment none", envEffort: "none", wantOnWire: "none"},
		{name: "yaml precedence", yamlEffort: "low", envEffort: "none", wantOnWire: "low"},
		{name: "blank omits", yamlEffort: "  ", wantOnWire: ""},
		{name: "invalid", yamlEffort: "private-invalid-value", wantErr: "ReasoningEffort"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var bodies []map[string]json.RawMessage
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error("cannot read request")
				}
				var fields map[string]json.RawMessage
				if json.Unmarshal(data, &fields) != nil {
					t.Error("request is not JSON")
				}
				mu.Lock()
				bodies = append(bodies, fields)
				mu.Unlock()
				serveCompletion(w, "result")
			}))
			defer server.Close()
			t.Setenv("OPENAI_REASONING_EFFORT", tc.envEffort)
			if tc.envEffort == "" {
				if err := os.Unsetenv("OPENAI_REASONING_EFFORT"); err != nil {
					t.Fatal(err)
				}
			}
			data := fmt.Sprintf("openai:\n  environment:\n    OPENAI_BASE_URL: %q\n    OPENAI_API_KEY: test-key\n", server.URL+"/v1")
			if tc.yamlEffort != "" {
				data += fmt.Sprintf("    OPENAI_REASONING_EFFORT: %q\n", tc.yamlEffort)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			client, _, err := configuredClient(path)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || strings.Contains(err.Error(), "private-invalid-value") {
					t.Fatalf("incorrect validation: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Prompt(context.Background(), "request"); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) != 1 {
				t.Fatalf("expected one request, got %d", len(bodies))
			}
			got, present := bodies[0]["reasoning_effort"]
			if tc.wantOnWire == "" {
				if present {
					t.Fatalf("reasoning_effort must be omitted, got %s", got)
				}
				return
			}
			if !present {
				t.Fatalf("reasoning_effort missing, want %q", tc.wantOnWire)
			}
			if string(got) != fmt.Sprintf("%q", tc.wantOnWire) {
				t.Fatalf("reasoning_effort = %s, want %q", got, tc.wantOnWire)
			}
		})
	}
}
