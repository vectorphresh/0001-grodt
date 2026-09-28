package openai_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	llm "github.com/vectorphresh/0001-grodt/internal/openai"
)

type wireRequest struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	ResponseFormat struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Schema      json.RawMessage `json:"schema"`
			Strict      *bool           `json:"strict"`
		} `json:"json_schema"`
	} `json:"response_format"`
}

func newTestClient(t *testing.T, handler http.HandlerFunc, timeout time.Duration) llm.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := llm.NewClient(llm.Config{BaseURL: server.URL + "/v1/", APIKey: rand.Text(), Timeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func completion(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": content}}}})
}

func readRequest(t *testing.T, r *http.Request) wireRequest {
	t.Helper()
	if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" {
		t.Error("incorrect endpoint or method")
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		t.Error("cannot read request")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		t.Error("request is not JSON")
	}
	if _, ok := fields["model"]; ok {
		t.Error("model must be omitted")
	}
	if _, ok := fields["stream"]; ok {
		t.Error("streaming must not be configured")
	}
	var req wireRequest
	if json.Unmarshal(data, &req) != nil {
		t.Error("cannot decode request")
	}
	return req
}

func specification() llm.JSONSpecification {
	return llm.JSONSpecification{Name: "cash_response", Description: "Return cash.", Schema: json.RawMessage(`{"type":"object","properties":{"cash":{"type":"integer"}},"required":["cash"],"additionalProperties":false}`), Strict: true}
}

func assertJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	// UseNumber ensures large caller-supplied integers are not silently rounded.
	decode := func(data []byte) any {
		d := json.NewDecoder(strings.NewReader(string(data)))
		d.UseNumber()
		var v any
		if err := d.Decode(&v); err != nil {
			t.Error("invalid JSON")
		}
		return v
	}
	if !reflect.DeepEqual(decode(got), decode([]byte(want))) {
		t.Error("JSON did not match expected value")
	}
}

func TestConstruction(t *testing.T) {
	key := rand.Text()
	for _, tc := range []struct {
		name, url, key string
		timeout        time.Duration
		valid          bool
	}{
		{"valid", "http://localhost:8080/v1", key, time.Second, true},
		{"default timeout", "https://localhost/v1", key, 0, true},
		{"empty URL", "", key, 0, false},
		{"relative URL", "/v1", key, 0, false},
		{"unsupported scheme", "ftp://localhost", key, 0, false},
		{"missing host", "http:///v1", key, 0, false},
		{"invalid URL", "http://%", key, 0, false},
		{"invalid port", "http://localhost:bad", key, 0, false},
		{"URL credentials", "http://user:" + key + "@localhost", key, 0, false},
		{"query", "http://localhost?v=1", key, 0, false},
		{"empty query", "http://localhost?", key, 0, false},
		{"fragment", "http://localhost#v1", key, 0, false},
		{"empty fragment", "http://localhost#", key, 0, false},
		{"negative timeout", "http://localhost", key, -time.Second, false},
		{"empty key", "http://localhost", "", 0, false},
		{"blank key", "http://localhost", " \t\n", 0, false},
		{"invalid key header", "http://localhost", key + "\r\n", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := llm.NewClient(llm.Config{BaseURL: tc.url, APIKey: tc.key, Timeout: tc.timeout})
			if (err == nil) != tc.valid || (c != nil) != tc.valid {
				t.Fatal("unexpected construction result")
			}
			if err != nil && strings.Contains(err.Error(), key) {
				t.Fatal("credential leaked")
			}
		})
	}
}

func TestPromptReturnsAssistantText(t *testing.T) {
	const prompt = " Explain this code.\n"
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := readRequest(t, r)
		if len(req.Messages) != 1 || req.Messages[0].Role != "user" || req.Messages[0].Content != prompt {
			t.Error("prompt not preserved")
		}
		if req.ResponseFormat.Type != "" {
			t.Error("vanilla prompt requested structured output")
		}
		completion(w, "  assistant text\n")
	}, 0)
	got, err := c.Prompt(context.Background(), prompt)
	if err != nil || got.Text != "  assistant text\n" {
		t.Fatalf("unexpected text result: %v", err)
	}
}

func TestPromptWithSpecificationReturnsStructuredJSON(t *testing.T) {
	for _, strict := range []bool{true, false} {
		t.Run(fmt.Sprint(strict), func(t *testing.T) {
			spec := specification()
			spec.Strict = strict
			// Numeric precision must survive both context and opaque schema translation.
			spec.Schema = json.RawMessage(`{"type":"object","properties":{"cash":{"type":"integer","const":9007199254740993}}}`)
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				req := readRequest(t, r)
				if len(req.Messages) != 2 {
					t.Error("expected two messages")
					return
				}
				if req.Messages[0].Role != "system" || req.Messages[0].Content != "Read context." || req.Messages[1].Role != "user" {
					t.Error("incorrect message roles or instructions")
				}
				assertJSON(t, []byte(req.Messages[1].Content), `{"prompt":"Return cash.","context":{"cash":9007199254740993}}`)
				f := req.ResponseFormat
				if f.Type != "json_schema" || f.JSONSchema.Name != spec.Name || f.JSONSchema.Description != spec.Description || f.JSONSchema.Strict == nil || *f.JSONSchema.Strict != strict {
					t.Error("incorrect specification translation")
				}
				assertJSON(t, f.JSONSchema.Schema, string(spec.Schema))
				completion(w, " {\"cash\":9007199254740993}\n")
			}, 0)
			got, err := c.PromptWithSpecification(context.Background(), "Read context.", "Return cash.", json.RawMessage(`{"cash":9007199254740993}`), spec)
			if err != nil || string(got.JSON) != " {\"cash\":9007199254740993}\n" {
				t.Fatalf("structured response changed: %v", err)
			}
		})
	}
}

func TestRequestMutation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		observation json.RawMessage
		want        string
	}{
		{"WithoutObservation", nil, "null"},
		{"EmptyObservation", json.RawMessage{}, "null"},
		{"ExplicitNull", json.RawMessage("null"), "null"},
		{"WithObservation", json.RawMessage(`{"event":"market_opened"}`), `{"event":"market_opened"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := json.RawMessage(`{"working_memory":[{"key":"market","value":"closed"}]}`)
			before, obsBefore := string(state), string(tc.observation)
			spec := specification()
			spec.Name = "state_mutation"
			const result = " {\"upsert_memory\":[{\"key\":\"market\",\"value\":\"open\"}]}\n"
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				req := readRequest(t, r)
				if len(req.Messages) != 2 {
					t.Error("expected two messages")
					return
				}
				if req.Messages[0].Role != "system" || req.Messages[0].Content != "Propose mutation." || req.Messages[1].Role != "user" {
					t.Error("incorrect mutation instructions")
				}
				assertJSON(t, []byte(req.Messages[1].Content), `{"state":`+before+`,"observation":`+tc.want+`}`)
				if req.ResponseFormat.JSONSchema.Name != "state_mutation" {
					t.Error("wrong mutation specification")
				}
				assertJSON(t, req.ResponseFormat.JSONSchema.Schema, string(spec.Schema))
				completion(w, result)
			}, 0)
			got, err := c.RequestMutation(context.Background(), "Propose mutation.", state, tc.observation, spec)
			if err != nil || string(got.JSON) != result {
				t.Fatalf("unexpected mutation result: %v", err)
			}
			if string(state) != before || string(tc.observation) != obsBefore {
				t.Fatal("caller data was modified")
			}
		})
	}
}

func TestInvalidInputsFailBeforeSending(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); completion(w, `{}`) }, 0)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"blank prompt", func() error { _, e := c.Prompt(ctx, " \n"); return e }},
		{"nil context", func() error { _, e := c.Prompt(nil, "hello"); return e }},
		{"blank structured prompt", func() error {
			_, e := c.PromptWithSpecification(ctx, "read", " ", json.RawMessage(`{}`), specification())
			return e
		}},
		{"blank instructions", func() error {
			_, e := c.PromptWithSpecification(ctx, " ", "read", json.RawMessage(`{}`), specification())
			return e
		}},
		{"missing context", func() error { _, e := c.PromptWithSpecification(ctx, "read", "read", nil, specification()); return e }},
		{"malformed context", func() error {
			_, e := c.PromptWithSpecification(ctx, "read", "read", json.RawMessage(`{`), specification())
			return e
		}},
		{"malformed state", func() error {
			_, e := c.RequestMutation(ctx, "read", json.RawMessage(`{`), nil, specification())
			return e
		}},
		{"missing state", func() error { _, e := c.RequestMutation(ctx, "read", nil, nil, specification()); return e }},
		{"malformed observation", func() error {
			_, e := c.RequestMutation(ctx, "read", json.RawMessage(`{}`), json.RawMessage(` `), specification())
			return e
		}},
		{"blank mutation instructions", func() error {
			_, e := c.RequestMutation(ctx, "", json.RawMessage(`{}`), nil, specification())
			return e
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.run() == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	for _, name := range []string{"", "invalid name", strings.Repeat("a", 65), "é"} {
		spec := specification()
		spec.Name = name
		if _, err := c.PromptWithSpecification(ctx, "read", "read", json.RawMessage(`{}`), spec); err == nil {
			t.Error("invalid name accepted")
		}
	}
	for _, schema := range []string{"", `{`, `{} {}`} {
		spec := specification()
		spec.Schema = json.RawMessage(schema)
		if _, err := c.RequestMutation(ctx, "read", json.RawMessage(`{}`), nil, spec); err == nil {
			t.Error("invalid schema accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid inputs reached provider")
	}
}

func TestMalformedProviderResponses(t *testing.T) {
	for _, body := range []string{
		`not JSON`, `null`, `{}`, `{"choices":[]}`,
		`{"choices":[{},{}]}`,
		`{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"partial"}}]}`,
		`{"choices":[{"message":{"role":"assistant","content":"text"}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"content":"text"}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"user","content":"text"}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":{}}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":" "}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"text","refusal":"refused"}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"text","tool_calls":[{}]}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"text","function_call":{}}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"text","refusal":{}}}]}`,
	} {
		t.Run(fmt.Sprint(len(body))+"_"+fmt.Sprint(strings.Index(body, "message")), func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, body)
			}, 0)
			if got, err := c.Prompt(context.Background(), "hello"); err == nil || got.Text != "" {
				t.Fatal("malformed completion accepted")
			}
		})
	}
}

func TestStructuredMethodsRejectMalformedJSON(t *testing.T) {
	for _, result := range []string{"", " ", "not JSON", "```json\n{}\n```", "{} {}", `{"cash":}`} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { completion(w, result) }, 0)
		if got, err := c.PromptWithSpecification(context.Background(), "read", "read", json.RawMessage(`{}`), specification()); err == nil || got.JSON != nil {
			t.Error("invalid structured result accepted")
		}
		if got, err := c.RequestMutation(context.Background(), "read", json.RawMessage(`{}`), nil, specification()); err == nil || got.JSON != nil {
			t.Error("invalid mutation result accepted")
		}
	}
}

func TestAuthenticationUsesOnlySuppliedConfiguration(t *testing.T) {
	key := rand.Text()
	t.Setenv("OPENAI_API_KEY", rand.Text())
	t.Setenv("OPENAI_BASE_URL", "http://invalid.invalid/")
	t.Setenv("OPENAI_CUSTOM_HEADERS", "X-Unwanted: inherited")
	t.Setenv("OPENAI_ORG_ID", "inherited-org")
	t.Setenv("OPENAI_PROJECT_ID", "inherited-project")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Error("supplied authentication not used")
		}
		for _, h := range []string{"X-Unwanted", "OpenAI-Organization", "OpenAI-Project"} {
			if r.Header.Get(h) != "" {
				t.Error("environment leaked into request")
			}
		}
		completion(w, "ok")
	}))
	defer s.Close()
	c, err := llm.NewClient(llm.Config{BaseURL: s.URL + "/v1", APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Prompt(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
}

func TestProviderErrorsAreSafeAndNotRetried(t *testing.T) {
	for _, status := range []int{400, 401, 408, 409, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			sensitive := rand.Text()
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("x-should-retry", "true")
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": r.Header.Get("Authorization") + sensitive}})
			}, 0)
			_, err := c.Prompt(context.Background(), "hello")
			if err == nil || !strings.Contains(err.Error(), fmt.Sprint(status)) {
				t.Fatal("expected HTTP status error")
			}
			if strings.Contains(err.Error(), sensitive) || strings.Contains(err.Error(), "Bearer") || errors.Unwrap(err) != nil {
				t.Fatal("provider diagnostics escaped public boundary")
			}
			if calls.Load() != 1 {
				t.Fatal("provider request retried")
			}
		})
	}
}

func TestContextCancellation(t *testing.T) {
	for _, phase := range []string{"before request", "during request", "during body"} {
		t.Run(phase, func(t *testing.T) {
			started := make(chan struct{})
			var calls atomic.Int32
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				if phase == "during body" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"choices":`)
					w.(http.Flusher).Flush()
				}
				close(started)
				<-r.Context().Done()
			}, time.Second)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if phase == "before request" {
				cancel()
			} else {
				go func() {
					select {
					case <-started:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			_, err := c.Prompt(ctx, "hello")
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation not preserved: %v", err)
			}
			if phase == "before request" && calls.Load() != 0 {
				t.Fatal("canceled request reached server")
			}
		})
	}
}

func TestRequestTimeout(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		clientTimeout, callerTimeout time.Duration
		body                         bool
	}{
		{"operation deadline", 50 * time.Millisecond, time.Second, false},
		{"earlier caller deadline", time.Second, 50 * time.Millisecond, false},
		{"response body", 50 * time.Millisecond, time.Second, true},
		{"default timeout respects caller", 0, 50 * time.Millisecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if tc.body {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"choices":`)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}, tc.clientTimeout)
			ctx, cancel := context.WithTimeout(context.Background(), tc.callerTimeout)
			defer cancel()
			start := time.Now()
			_, err := c.Prompt(ctx, "hello")
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline not preserved: %v", err)
			}
			if time.Since(start) > 750*time.Millisecond {
				t.Fatal("earliest deadline was not honored")
			}
		})
	}
}

func TestConcurrentInteractionsAreIndependent(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := readRequest(t, r)
		if len(req.Messages) != 1 {
			t.Error("unexpected history")
			return
		}
		completion(w, req.Messages[0].Content)
	}, 0)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			prompt := fmt.Sprintf("request %d", i)
			got, err := c.Prompt(context.Background(), prompt)
			if err != nil || got.Text != prompt {
				t.Error("interaction mixed or failed")
			}
		}(i)
	}
	wg.Wait()
}

func TestUsageTravelsWithEachResponse(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		want        *llm.Usage
	}{
		{"present", `{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}`, &llm.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}},
		{"zero", `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`, &llm.Usage{}},
		{"missing", "", nil}, {"null", "null", nil}, {"empty", `{}`, nil},
		{"partial", `{"prompt_tokens":11,"total_tokens":18}`, nil},
		{"wrong type", `{"prompt_tokens":"11","completion_tokens":7,"total_tokens":18}`, nil},
		{"fractional", `{"prompt_tokens":1.5,"completion_tokens":7,"total_tokens":8}`, nil},
		{"negative", `{"prompt_tokens":-1,"completion_tokens":7,"total_tokens":6}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				body := `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{}"}}]`
				if tc.usage != "" {
					body += `,"usage":` + tc.usage
				}
				io.WriteString(w, body+`}`)
			}, 0)
			text, err := c.Prompt(context.Background(), "hello")
			if err != nil {
				t.Fatal(err)
			}
			structured, err := c.PromptWithSpecification(context.Background(), "read", "read", json.RawMessage(`{}`), specification())
			if err != nil {
				t.Fatal(err)
			}
			mutation, err := c.RequestMutation(context.Background(), "read", json.RawMessage(`{}`), nil, specification())
			if err != nil {
				t.Fatal(err)
			}
			for _, u := range []*llm.Usage{text.Usage, structured.Usage, mutation.Usage} {
				if !reflect.DeepEqual(u, tc.want) {
					t.Fatal("incorrect usage or fabricated availability")
				}
			}
			if text.Text != "{}" || string(structured.JSON) != "{}" || string(mutation.JSON) != "{}" {
				t.Fatal("usage changed content")
			}
			if text.Usage != nil {
				text.Usage.PromptTokens = 99
				if structured.Usage.PromptTokens == 99 || mutation.Usage.PromptTokens == 99 {
					t.Fatal("usage shared across results")
				}
			}
		})
	}
}
