package tools

import (
	"encoding/json"
)

// ToolDefinition represents a tool definition
type ToolDefinition struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
	Source       string          `json:"source,omitempty"`
}

// ToolCall represents a tool call
type ToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolResult represents a tool result
type ToolResult struct {
	Tool string          `json:"tool"`
	Data json.RawMessage `json:"data"`
}
