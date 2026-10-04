// Package toolcall defines provider-neutral native tool conversations.
package toolcall

import (
	"context"
	"encoding/json"
)

type Definition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}
type Call struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
type Message struct {
	Role   string `json:"role"`
	Text   string `json:"text,omitempty"`
	Calls  []Call `json:"calls,omitempty"`
	CallID string `json:"call_id,omitempty"`
}
type Request struct {
	Instructions string
	Messages     []Message
	Tools        []Definition
}
type Response struct {
	Text                                        string
	Calls                                       []Call
	PromptTokens, CompletionTokens, TotalTokens int64
	UsageAvailable                              bool
}
type Client interface {
	GenerateWithTools(context.Context, Request) (Response, error)
}
