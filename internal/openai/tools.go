package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

// GenerateWithTools is separate from the existing text/structured decoders,
// whose contracts continue to reject native tool requests.
func (c *client) GenerateWithTools(ctx context.Context, request toolcall.Request) (toolcall.Response, error) {
	if ctx == nil {
		return toolcall.Response{}, errors.New("openai: context required")
	}
	messages := []map[string]any{{"role": "system", "content": request.Instructions}}
	for _, m := range request.Messages {
		v := map[string]any{"role": m.Role, "content": m.Text}
		switch m.Role {
		case "assistant":
			if len(m.Calls) > 0 {
				calls := make([]map[string]any, 0, len(m.Calls))
				for _, call := range m.Calls {
					calls = append(calls, map[string]any{"id": call.ID, "type": "function", "function": map[string]any{"name": call.Name, "arguments": string(call.Arguments)}})
				}
				v["tool_calls"] = calls
			}
		case "tool":
			v["tool_call_id"] = m.CallID
		case "user":
		default:
			return toolcall.Response{}, errors.New("openai: invalid tool message role")
		}
		messages = append(messages, v)
	}
	definitions := make([]map[string]any, 0, len(request.Tools))
	for _, t := range request.Tools {
		if !json.Valid(t.InputSchema) {
			return toolcall.Response{}, errors.New("openai: invalid tool schema")
		}
		definitions = append(definitions, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": t.InputSchema}})
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	// JSON options avoid importing provider message types into the generic API.
	options := []option.RequestOption{option.WithJSONSet("messages", messages)}
	if len(definitions) > 0 {
		options = append(options, option.WithJSONSet("tools", definitions))
	}
	response, err := c.service.New(ctx, sdk.ChatCompletionNewParams{}, options...)
	if ctx.Err() != nil {
		return toolcall.Response{}, fmt.Errorf("openai: request failed: %w", ctx.Err())
	}
	if err != nil {
		return toolcall.Response{}, requestError(err)
	}
	if response == nil || len(response.Choices) != 1 {
		return toolcall.Response{}, errors.New("openai: expected one tool response choice")
	}
	choice := response.Choices[0]

	var message struct {
		Role     *string         `json:"role"`
		Content  *string         `json:"content"`
		Refusal  *string         `json:"refusal"`
		Function json.RawMessage `json:"function_call"`
		Calls    []struct {
			ID       *string `json:"id"`
			Type     *string `json:"type"`
			Function *struct {
				Name      *string `json:"name"`
				Arguments *string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if json.Unmarshal([]byte(choice.Message.RawJSON()), &message) != nil || message.Role == nil || *message.Role != "assistant" || (message.Refusal != nil && *message.Refusal != "") || (len(message.Function) > 0 && string(message.Function) != "null") {
		return toolcall.Response{}, errors.New("openai: malformed or unsupported tool response")
	}
	out := toolcall.Response{}
	if message.Content != nil {
		out.Text = *message.Content
	}
	if len(message.Calls) == 0 && strings.TrimSpace(out.Text) == "" {
		return toolcall.Response{}, errors.New("openai: empty final tool response")
	}
	if usage := extractUsage(response); usage != nil {
		out.UsageAvailable = true
		out.PromptTokens = usage.PromptTokens
		out.CompletionTokens = usage.CompletionTokens
		out.TotalTokens = usage.TotalTokens
	}
	for _, call := range message.Calls {
		if call.ID == nil || call.Type == nil || *call.Type != "function" || call.Function == nil || call.Function.Name == nil || call.Function.Arguments == nil {
			return toolcall.Response{}, errors.New("openai: malformed tool call")
		}
		out.Calls = append(out.Calls, toolcall.Call{ID: *call.ID, Name: *call.Function.Name, Arguments: json.RawMessage(*call.Function.Arguments)})
	}
	if (len(out.Calls) > 0 && choice.FinishReason != "tool_calls") || (len(out.Calls) == 0 && choice.FinishReason != "stop") {
		return toolcall.Response{}, errors.New("openai: incomplete tool response")
	}
	return out, nil
}
