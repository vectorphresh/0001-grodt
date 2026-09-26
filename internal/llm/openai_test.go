package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const successfulEnvelope = `{"id":"test","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"{\"done\":true}"}}],"usage":{"total_tokens":10}}`

func testClient(t *testing.T, base, key string, timeout time.Duration) *OpenAIClient {
	t.Helper()
	client, err := NewOpenAIClient(OpenAIConfig{BaseURL: base, Model: "configured-model", APIKey: key, RequestTimeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestOpenAIRequestAndSuccessfulResponse(t *testing.T) {
	for _, key := range []string{"", "test-secret"} {
		for _, path := range []string{"", "/v1", "/gateway/v1/"} {
			t.Run(fmt.Sprintf("auth=%t/path=%s", key != "", path), func(t *testing.T) {
				input := CompletionRequest{Messages: []Message{{Role: "system", Content: "Return a JSON decision."}, {Role: "user", Content: `{"observation":{"data":"Unicode: € and 漢字"}}`}}}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPost || r.URL.Path != strings.TrimRight(path, "/")+"/chat/completions" {
						t.Errorf("request: %s %s", r.Method, r.URL.Path)
					}
					if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
						t.Error("missing JSON headers")
					}
					auth, exists := r.Header["Authorization"]
					if key == "" && exists {
						t.Error("Authorization must be absent")
					}
					if key != "" && (!exists || len(auth) != 1 || auth[0] != "Bearer "+key) {
						t.Error("incorrect Authorization")
					}
					var payload struct {
						Model    string    `json:"model"`
						Messages []Message `json:"messages"`
						Stream   bool      `json:"stream"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
					}
					if payload.Model != "configured-model" || payload.Stream || !reflect.DeepEqual(payload.Messages, input.Messages) {
						t.Errorf("payload: %+v", payload)
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, successfulEnvelope)
				}))
				defer server.Close()
				response, err := testClient(t, server.URL+path, key, time.Second).Complete(context.Background(), input)
				if err != nil || response.Content != `{"done":true}` {
					t.Fatalf("response=%+v err=%v", response, err)
				}
			})
		}
	}
}

func TestRejectMalformedAndEmptyResponses(t *testing.T) {
	cases := map[string]string{
		"malformed JSON":          `{`,
		"trailing JSON":           successfulEnvelope + `{}`,
		"null envelope":           `null`,
		"array envelope":          `[]`,
		"missing choices":         `{}`,
		"null choices":            `{"choices":null}`,
		"empty choices":           `{"choices":[]}`,
		"wrong choices type":      `{"choices":{}}`,
		"missing message":         `{"choices":[{}]}`,
		"null message":            `{"choices":[{"message":null}]}`,
		"wrong role":              `{"choices":[{"message":{"role":"user","content":"{}"}}]}`,
		"missing role":            `{"choices":[{"message":{"content":"{}"}}]}`,
		"missing content":         `{"choices":[{"message":{"role":"assistant"}}]}`,
		"null content":            `{"choices":[{"message":{"role":"assistant","content":null}}]}`,
		"array content":           `{"choices":[{"message":{"role":"assistant","content":[]}}]}`,
		"empty content":           `{"choices":[{"message":{"role":"assistant","content":""}}]}`,
		"whitespace content":      `{"choices":[{"message":{"role":"assistant","content":" \n\t"}}]}`,
		"truncated completion":    `{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"{}"}}]}`,
		"API error in 200":        `{"error":{"message":"test-secret"}}`,
		"mixed error and success": `{"error":{"message":"test-secret"},"choices":[{"message":{"role":"assistant","content":"{}"}}]}`,
		"multiple choices":        `{"choices":[{"message":{"role":"assistant","content":"{}"}},{"message":{"role":"assistant","content":"{}"}}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			_, err := testClient(t, server.URL, "test-secret", time.Second).Complete(context.Background(), CompletionRequest{})
			var transport *TransportError
			if !errors.As(err, &transport) {
				t.Fatalf("expected transport error, got %v", err)
			}
			if strings.Contains(err.Error(), "test-secret") {
				t.Fatal("credential exposed in error")
			}
		})
	}
}

func TestMinimalCompatibleEnvelopeAndContentPreserved(t *testing.T) {
	content := "  {\"done\":true}\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "vendor_metadata": "ignored"}}})
	}))
	defer server.Close()
	response, err := testClient(t, server.URL, "", time.Second).Complete(context.Background(), CompletionRequest{})
	if err != nil || response.Content != content {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestNon2xxResponses(t *testing.T) {
	for _, status := range []int{301, 400, 401, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":"test-secret"}`)
			}))
			defer server.Close()
			_, err := testClient(t, server.URL, "test-secret", time.Second).Complete(context.Background(), CompletionRequest{})
			var transport *TransportError
			if !errors.As(err, &transport) || transport.StatusCode != status {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(err.Error(), "test-secret") {
				t.Fatal("error echoed credentials")
			}
		})
	}
}

func TestResponseBodyBound(t *testing.T) {
	// Use padding metadata so the response at exactly the limit remains valid JSON.
	prefix := `{"padding":"`
	suffix := `","choices":[{"message":{"role":"assistant","content":"{}"}}]}`
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			body := prefix + strings.Repeat("x", int(MaxCompletionResponseBytes)-len(prefix)-len(suffix)+extra) + suffix
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush() // No Content-Length: exercise the streaming read limit.
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			_, err := testClient(t, server.URL, "", time.Second).Complete(context.Background(), CompletionRequest{})
			if (err == nil) != (extra == 0) {
				t.Fatalf("limit result: %v", err)
			}
		})
	}
}

func TestContextCancellationAndRequestTimeout(t *testing.T) {
	for _, bodyStarted := range []bool{false, true} {
		for _, timeout := range []bool{false, true} {
			t.Run(fmt.Sprintf("body=%t/timeout=%t", bodyStarted, timeout), func(t *testing.T) {
				started := make(chan struct{})
				serverCanceled := make(chan struct{})
				release := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					io.Copy(io.Discard, r.Body)
					if bodyStarted {
						w.WriteHeader(200)
						w.(http.Flusher).Flush()
					}
					close(started)
					select {
					case <-r.Context().Done():
						close(serverCanceled)
					case <-release:
					}
				}))
				defer server.Close()
				defer close(release)
				duration := 5 * time.Second
				if timeout {
					duration = 100 * time.Millisecond
				}
				client := testClient(t, server.URL, "", duration)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				result := make(chan error, 1)
				go func() { _, err := client.Complete(ctx, CompletionRequest{}); result <- err }()
				select {
				case <-started:
				case <-time.After(2 * time.Second):
					t.Fatal("request never started")
				}
				expected := context.DeadlineExceeded
				if !timeout {
					cancel()
					expected = context.Canceled
				}
				select {
				case err := <-result:
					if !errors.Is(err, expected) {
						t.Fatalf("error=%v; expected %v", err, expected)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("client ignored cancellation/timeout")
				}
				select {
				case <-serverCanceled:
				case <-time.After(2 * time.Second):
					t.Fatal("server request context not canceled")
				}
			})
		}
	}
}

func TestCanceledContextMakesNoRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := testClient(t, server.URL, "", time.Second).Complete(ctx, CompletionRequest{})
	if !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatalf("error=%v calls=%d", err, calls.Load())
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	_, err := testClient(t, server.URL, "test-secret", time.Second).Complete(context.Background(), CompletionRequest{})
	var transport *TransportError
	if !errors.As(err, &transport) || transport.StatusCode != 307 || targetCalls.Load() != 0 {
		t.Fatalf("redirect: %v target calls=%d", err, targetCalls.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNetworkDiagnosticsDoNotExposeSecret(t *testing.T) {
	client := testClient(t, "http://localhost/v1", "test-secret", time.Second)
	cause := errors.New("network error with test-secret")
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, cause })
	_, err := client.Complete(context.Background(), CompletionRequest{})
	if err == nil || strings.Contains(err.Error(), "test-secret") || !errors.Is(err, cause) {
		t.Fatalf("unsafe or unclassified error: %v", err)
	}
}
