package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

type client struct {
	service sdk.ChatCompletionService
	timeout time.Duration
}

var _ Client = (*client)(nil)

// NewClient validates configuration without contacting the endpoint. It owns an
// HTTP client and transport, injected into the SDK solely as a protocol binding.
// SDK retries are disabled and the model field is omitted for endpoint selection.
func NewClient(config Config) (Client, error) {
	u, err := url.Parse(config.BaseURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Opaque != "" {
		return nil, errors.New("openai: BaseURL must be an absolute HTTP(S) API root")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(config.BaseURL, "#") {
		return nil, errors.New("openai: BaseURL cannot contain credentials, a query, or a fragment")
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("openai: APIKey is required")
	}
	for _, c := range config.APIKey {
		if c < 0x20 || c > 0x7e {
			return nil, errors.New("openai: APIKey contains invalid header characters")
		}
	}
	if config.Timeout < 0 {
		return nil, errors.New("openai: Timeout must not be negative")
	}
	if config.Timeout == 0 {
		config.Timeout = 2 * time.Minute
	}
	// Own the connection pool. Do not mutate the process-wide default transport.
	// An explicit fallback also avoids assuming an application's global transport
	// replacement is an *http.Transport.
	transport := &http.Transport{}
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	}
	httpClient := &http.Client{Transport: transport} // Timeout deliberately zero.
	service := sdk.NewChatCompletionService(
		option.WithHTTPClient(httpClient),
		option.WithBaseURL(strings.TrimRight(u.String(), "/")+"/"),
		option.WithAPIKey(config.APIKey),
		option.WithMaxRetries(0),
		option.WithJSONDel("model"),
	)
	return &client{service: service, timeout: config.Timeout}, nil
}

func (c *client) Prompt(ctx context.Context, prompt string) (TextResult, error) {
	if strings.TrimSpace(prompt) == "" {
		return TextResult{}, errors.New("openai: prompt must not be blank")
	}
	return c.complete(ctx, sdk.ChatCompletionNewParams{Messages: []sdk.ChatCompletionMessageParamUnion{sdk.UserMessage(prompt)}})
}

func (c *client) PromptWithSpecification(ctx context.Context, instructions, prompt string, input json.RawMessage, specification JSONSpecification) (JSONResult, error) {
	if strings.TrimSpace(prompt) == "" {
		return JSONResult{}, errors.New("openai: prompt must not be blank")
	}
	if !json.Valid(input) {
		return JSONResult{}, errors.New("openai: context must contain valid JSON")
	}
	payload, err := json.Marshal(struct {
		Prompt  string          `json:"prompt"`
		Context json.RawMessage `json:"context"`
	}{prompt, input})
	if err != nil {
		return JSONResult{}, errors.New("openai: cannot encode context")
	}
	return c.structured(ctx, instructions, payload, specification)
}

func (c *client) RequestMutation(ctx context.Context, instructions string, state, observation json.RawMessage, specification JSONSpecification) (JSONResult, error) {
	if !json.Valid(state) {
		return JSONResult{}, errors.New("openai: state must contain valid JSON")
	}
	if len(observation) == 0 {
		observation = json.RawMessage("null")
	}
	if !json.Valid(observation) {
		return JSONResult{}, errors.New("openai: observation must contain valid JSON")
	}
	payload, err := json.Marshal(struct {
		State       json.RawMessage `json:"state"`
		Observation json.RawMessage `json:"observation"`
	}{state, observation})
	if err != nil {
		return JSONResult{}, errors.New("openai: cannot encode state and observation")
	}
	return c.structured(ctx, instructions, payload, specification)
}

func (c *client) structured(ctx context.Context, instructions string, payload []byte, specification JSONSpecification) (JSONResult, error) {
	params, err := structuredParams(instructions, payload, specification)
	if err != nil {
		return JSONResult{}, err
	}
	text, err := c.complete(ctx, params)
	if err != nil {
		return JSONResult{}, err
	}
	if !json.Valid([]byte(text.Text)) {
		return JSONResult{}, errors.New("openai: structured response is not valid JSON")
	}
	return JSONResult{JSON: json.RawMessage(text.Text), Usage: text.Usage}, nil
}

func (c *client) complete(ctx context.Context, params sdk.ChatCompletionNewParams) (TextResult, error) {
	if ctx == nil {
		return TextResult{}, errors.New("openai: context is required")
	}
	// The sole interaction timeout covers SDK execution and response reading.
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	response, err := c.service.New(ctx, params)
	if ctx.Err() != nil {
		return TextResult{}, fmt.Errorf("openai: request failed: %w", ctx.Err())
	}
	if err != nil {
		return TextResult{}, requestError(err)
	}
	text, err := extractText(response)
	if err != nil {
		return TextResult{}, err
	}
	return TextResult{Text: text, Usage: extractUsage(response)}, nil
}

// Preserve context classification, but never retain an SDK error (which can hold
// credentials and provider bodies) in the public error chain.
func requestError(err error) error {
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, sentinel) {
			return fmt.Errorf("openai: request failed: %w", sentinel)
		}
	}
	var provider *sdk.Error
	if errors.As(err, &provider) {
		return fmt.Errorf("openai: provider request failed (HTTP %d)", provider.StatusCode)
	}
	return errors.New("openai: request or response processing failed")
}
