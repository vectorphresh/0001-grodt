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
	"sync/atomic"
	"testing"
)

func TestCLIStateFromConfigurationAndOverride(t *testing.T) {
	modulePath, err := filepath.Abs("../../modules/mcp-tool-state/state.json")
	if err != nil {
		t.Fatal(err)
	}
	counterPath, err := filepath.Abs("../../internal/state/wasm/testdata/state.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name         string
		flags        []string
		want, absent string
	}{
		{name: "config relative path", want: `"mcp_tools"`, absent: `"counter"`},
		{name: "CLI override", flags: []string{"--state-definition", counterPath}, want: `"counter"`, absent: `"mcp_tools"`},
		{name: "explicit empty override", flags: []string{"--state-definition="}, want: `"knowledge":{}`, absent: `"mcp_tools"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error("invalid request")
				}
				combined := ""
				for _, m := range request.Messages {
					combined += m.Content
				}
				if !strings.Contains(combined, tt.want) || strings.Contains(combined, tt.absent) {
					t.Error("incorrect state module reached inference")
				}
				if calls.Add(1) == 1 {
					serveCompletion(w, "done")
				} else {
					serveCompletion(w, `{"achieved":true,"rationale":"done"}`)
				}
			}))
			defer server.Close()
			path := configFile(t, "")
			relative, err := filepath.Rel(filepath.Dir(path), modulePath)
			if err != nil {
				t.Fatal(err)
			}
			content := fmt.Sprintf("openai:\n  environment:\n    OPENAI_BASE_URL: %q\n    OPENAI_API_KEY: test-key\nstate:\n  definition: %q\n", server.URL+"/v1", relative)
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"--config", path}, tt.flags...)
			args = append(args, "request")
			if err := run(context.Background(), args, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 2 {
				t.Fatal("unexpected inference count")
			}
		})
	}
}
