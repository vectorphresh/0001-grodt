package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/agent"
	"github.com/vectorphresh/0001-grodt/internal/llm"
)

func completion(w http.ResponseWriter, content string) {
	json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
}

func TestHTTPAcceptanceFlow(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		number := calls.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Method != "POST" {
			t.Errorf("request: %s %s", r.Method, r.URL.Path)
		}
		var req struct {
			Model    string        `json:"model"`
			Messages []llm.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Model != "test-model" || len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
			t.Errorf("unexpected request: %+v", req)
			return
		}
		var input agent.StepInput
		if err := json.Unmarshal([]byte(req.Messages[1].Content), &input); err != nil {
			t.Error(err)
			return
		}
		if input.Spec != fakeAccountSpec() {
			t.Errorf("run specification missing or changed: %+v", input.Spec)
		}
		switch number {
		case 1:
			if len(input.Tools) != 1 || input.Tools[0].Name != "fake.get_account" || input.Observation != nil {
				t.Errorf("first input: %+v", input)
			}
			completion(w, `{"intent":"inspect account","tool_call":{"name":"fake.get_account","arguments":{}}}`)
		case 2:
			if input.Observation == nil || input.Observation.Error != nil || input.Observation.Tool != "fake.get_account" || string(input.Observation.Data) != `{"status":"active"}` || input.State.Version != 1 {
				t.Errorf("second input: %+v", input)
			}
			completion(w, `{"summary":"Account is active","done":true}`)
		default:
			t.Error("unexpected retry")
			http.Error(w, "unexpected retry", 500)
		}
	}))
	defer server.Close()
	var output bytes.Buffer
	err := runLLM(context.Background(), llm.OpenAIConfig{BaseURL: server.URL + "/v1", Model: "test-model", RequestTimeout: time.Second}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || strings.Count(output.String(), "PASS:") != 7 {
		t.Fatalf("calls=%d output=%s", calls.Load(), output.String())
	}
}

func TestAcceptanceCannotSucceedWithoutToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { completion(w, `{"done":true}`) }))
	defer server.Close()
	var output bytes.Buffer
	err := runLLM(context.Background(), llm.OpenAIConfig{BaseURL: server.URL, Model: "test", RequestTimeout: time.Second}, &output)
	if err == nil || output.Len() != 0 {
		t.Fatalf("false acceptance: %v %s", err, output.String())
	}
}

func TestAcceptanceStopsAfterTwoModelRequests(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); completion(w, `not a decision`) }))
	defer server.Close()
	var output bytes.Buffer
	err := runLLM(context.Background(), llm.OpenAIConfig{BaseURL: server.URL, Model: "test", RequestTimeout: time.Second}, &output)
	if err == nil || calls.Load() != 2 || output.Len() != 0 {
		t.Fatalf("budget: %v requests=%d", err, calls.Load())
	}
}

func TestCLIEnvironmentAndFlagPrecedence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string        `json:"model"`
			Messages []llm.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Model != "flag-model" || r.Header.Get("Authorization") != "Bearer test-cli-key" {
			t.Error("CLI configuration not applied")
		}
		var input agent.StepInput
		if err := json.Unmarshal([]byte(req.Messages[1].Content), &input); err != nil {
			t.Error(err)
			return
		}
		if input.Observation == nil {
			completion(w, `{"tool_call":{"name":"fake.get_account","arguments":{}}}`)
		} else {
			completion(w, `{"done":true}`)
		}
	}))
	defer server.Close()
	t.Setenv("GRODT_LLM_BASE_URL", "http://unreachable.invalid")
	t.Setenv("GRODT_LLM_MODEL", "env-model")
	t.Setenv("GRODT_LLM_API_KEY", "test-cli-key")
	t.Setenv("GRODT_LLM_REQUEST_TIMEOUT", "1ns")
	var output, diagnostics bytes.Buffer
	if err := run(context.Background(), []string{"-mode", "llm", "-base-url", server.URL, "-model", "flag-model", "-request-timeout", "2s"}, &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String()+diagnostics.String(), "test-cli-key") {
		t.Fatal("API key logged")
	}
}

func TestFakeDemoStillWorksWithoutModelConfiguration(t *testing.T) {
	t.Setenv("GRODT_LLM_MODEL", "")
	var output, diagnostics bytes.Buffer
	if err := run(context.Background(), nil, &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "2 decisions, 1 tool call, state version 2") {
		t.Fatal(output.String())
	}
}

func TestTransportFailureStopsAcceptance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401); fmt.Fprint(w, "test-key") }))
	defer server.Close()
	var output bytes.Buffer
	err := runLLM(context.Background(), llm.OpenAIConfig{BaseURL: server.URL, Model: "test", APIKey: "test-key", RequestTimeout: time.Second}, &output)
	if err == nil || strings.Contains(err.Error(), "test-key") || output.Len() != 0 {
		t.Fatalf("error handling: %v", err)
	}
}
