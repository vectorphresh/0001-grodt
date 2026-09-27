package openai

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

var specificationName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func structuredParams(instructions string, payload []byte, specification JSONSpecification) (sdk.ChatCompletionNewParams, error) {
	if strings.TrimSpace(instructions) == "" {
		return sdk.ChatCompletionNewParams{}, errors.New("openai: instructions must not be blank")
	}
	if !specificationName.MatchString(specification.Name) {
		return sdk.ChatCompletionNewParams{}, errors.New("openai: specification name must be 1-64 ASCII letters, digits, underscores, or dashes")
	}
	if !json.Valid(specification.Schema) {
		return sdk.ChatCompletionNewParams{}, errors.New("openai: schema must contain valid JSON")
	}
	schema := shared.ResponseFormatJSONSchemaJSONSchemaParam{
		Name: specification.Name, Schema: specification.Schema, Strict: sdk.Bool(specification.Strict),
	}
	if specification.Description != "" {
		schema.Description = sdk.String(specification.Description)
	}
	return sdk.ChatCompletionNewParams{
		Messages:       []sdk.ChatCompletionMessageParamUnion{sdk.SystemMessage(instructions), sdk.UserMessage(string(payload))},
		ResponseFormat: sdk.ChatCompletionNewParamsResponseFormatUnion{OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{JSONSchema: schema}},
	}, nil
}

func extractText(response *sdk.ChatCompletion) (string, error) {
	if response == nil || !response.JSON.Choices.Valid() || len(response.Choices) != 1 {
		return "", errors.New("openai: expected one completion choice")
	}
	choice := response.Choices[0]
	message := choice.Message
	if !choice.JSON.FinishReason.Valid() || choice.FinishReason != "stop" {
		return "", errors.New("openai: completion did not finish normally")
	}
	if !choice.JSON.Message.Valid() || !message.JSON.Role.Valid() || message.Role != "assistant" || !message.JSON.Content.Valid() {
		return "", errors.New("openai: expected an assistant text response")
	}
	if message.Refusal != "" || len(message.ToolCalls) != 0 || message.JSON.FunctionCall.Valid() {
		return "", errors.New("openai: response contains a refusal or tool call")
	}
	// Reject malformed optional fields even when the SDK tolerates their types.
	for _, field := range []struct {
		raw   string
		valid bool
	}{
		{message.JSON.Refusal.Raw(), message.JSON.Refusal.Valid()},
		{message.JSON.ToolCalls.Raw(), message.JSON.ToolCalls.Valid()},
		{message.JSON.FunctionCall.Raw(), message.JSON.FunctionCall.Valid()},
	} {
		if field.raw != "" && field.raw != "null" && !field.valid {
			return "", errors.New("openai: malformed assistant response")
		}
	}
	if strings.TrimSpace(message.Content) == "" {
		return "", errors.New("openai: assistant text is empty")
	}
	return message.Content, nil
}
