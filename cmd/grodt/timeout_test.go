package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfiguredRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
			serveCompletion(w, "result")
		}
	}))
	defer server.Close()
	for _, tc := range []struct {
		name, yamlTimeout, envTimeout string
		wantTimeout, wantInvalid      bool
	}{
		{name: "default"},
		{name: "yaml deadline", yamlTimeout: "20ms", wantTimeout: true},
		{name: "environment deadline", envTimeout: "20ms", wantTimeout: true},
		{name: "yaml precedence", yamlTimeout: "2s", envTimeout: "20ms"},
		{name: "invalid", yamlTimeout: "private-invalid-value", wantInvalid: true},
		{name: "zero", yamlTimeout: "0s", wantInvalid: true},
		{name: "negative", yamlTimeout: "-1s", wantInvalid: true},
		{name: "blank", yamlTimeout: " ", wantInvalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPENAI_TIMEOUT", tc.envTimeout)
			if tc.envTimeout == "" {
				if err := os.Unsetenv("OPENAI_TIMEOUT"); err != nil {
					t.Fatal(err)
				}
			}
			data := fmt.Sprintf("openai:\n  environment:\n    OPENAI_BASE_URL: %q\n    OPENAI_API_KEY: test-key\n", server.URL+"/v1")
			if tc.yamlTimeout != "" {
				data += fmt.Sprintf("    OPENAI_TIMEOUT: %q\n", tc.yamlTimeout)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			client, _, err := configuredClient(path)
			if tc.wantInvalid {
				if err == nil || !strings.Contains(err.Error(), "OPENAI_TIMEOUT must be a positive duration") || strings.Contains(err.Error(), "private-invalid-value") {
					t.Fatalf("incorrect validation: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Prompt(context.Background(), "request")
			if tc.wantTimeout {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("expected deadline: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
