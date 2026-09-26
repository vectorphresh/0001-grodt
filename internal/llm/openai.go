package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// MaxCompletionResponseBytes bounds decoded response bodies, including responses
// decompressed by net/http. Requests are nonstreaming and never retried here.
const MaxCompletionResponseBytes int64 = 1 << 20

// TransportError contains safe diagnostics. It never prints headers, URLs, raw
// response bodies, or underlying error text. Unwrap preserves cancellation and
// timeout classification with errors.Is/errors.As.
type TransportError struct {
	Operation  string
	StatusCode int
	cause      error
}

func (e *TransportError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("LLM transport: %s (HTTP %d)", e.Operation, e.StatusCode)
	}
	return "LLM transport: " + e.Operation
}
func (e *TransportError) Unwrap() error { return e.cause }

// OpenAIClient implements the Chat Completions text boundary with net/http.
// Decisions and tool execution remain the responsibility of agent and runtime.
type OpenAIClient struct {
	endpoint   string
	model      string
	apiKey     string
	httpClient *http.Client
}

func NewOpenAIClient(config OpenAIConfig) (*OpenAIClient, error) {
	endpoint, err := config.endpoint()
	if err != nil {
		return nil, err
	}
	return &OpenAIClient{
		endpoint: endpoint, model: config.Model, apiKey: config.APIKey,
		httpClient: &http.Client{
			Timeout: config.RequestTimeout,
			// Treat redirects as non-2xx responses, rather than forwarding credentials
			// or silently changing the method/endpoint selected by configuration.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (c *OpenAIClient) Complete(ctx context.Context, input CompletionRequest) (CompletionResponse, error) {
	payload, err := json.Marshal(struct {
		Model    string    `json:"model"`
		Messages []Message `json:"messages"`
		Stream   bool      `json:"stream"`
	}{Model: c.model, Messages: input.Messages, Stream: false})
	if err != nil {
		return CompletionResponse{}, &TransportError{Operation: "encode request", cause: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return CompletionResponse{}, &TransportError{Operation: "create request", cause: err}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return CompletionResponse{}, &TransportError{Operation: "request failed", cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return CompletionResponse{}, &TransportError{Operation: "unexpected status", StatusCode: response.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxCompletionResponseBytes+1))
	if err != nil {
		return CompletionResponse{}, &TransportError{Operation: "read response", cause: err}
	}
	if int64(len(body)) > MaxCompletionResponseBytes {
		return CompletionResponse{}, &TransportError{Operation: "response exceeds 1 MiB limit"}
	}
	// Accept unknown vendor metadata while requiring the text-completion envelope.
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Message *struct {
				Role    string  `json:"role"`
				Content *string `json:"content"`
			} `json:"message"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return CompletionResponse{}, &TransportError{Operation: "malformed response JSON", cause: err}
	}
	if (len(envelope.Error) > 0 && string(envelope.Error) != "null") || len(envelope.Choices) != 1 {
		return CompletionResponse{}, &TransportError{Operation: "malformed completion envelope"}
	}
	choice := envelope.Choices[0]
	if choice.Message == nil || choice.Message.Role != "assistant" || choice.Message.Content == nil {
		return CompletionResponse{}, &TransportError{Operation: "missing assistant text completion"}
	}
	if choice.FinishReason != nil && *choice.FinishReason != "stop" {
		return CompletionResponse{}, &TransportError{Operation: "completion did not finish normally"}
	}
	if strings.TrimSpace(*choice.Message.Content) == "" {
		return CompletionResponse{}, &TransportError{Operation: "empty completion"}
	}
	return CompletionResponse{Content: *choice.Message.Content}, nil
}
