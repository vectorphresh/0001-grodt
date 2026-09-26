package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vectorphresh/0001-grodt/internal/tools"
)

func (p *Provider) Execute(ctx context.Context, call tools.ToolCall) (tools.ToolResult, error) {
	callCtx, finish, err := p.callContext(ctx)
	if err != nil {
		return tools.ToolResult{}, err
	}
	defer finish()
	// Keep the raw JSON, including integer precision, when crossing the protocol.
	result, err := p.session.CallTool(callCtx, &mcp.CallToolParams{Name: call.Name, Arguments: call.Arguments})
	if err != nil {
		return tools.ToolResult{}, classifyCallError(callCtx, err)
	}
	if result == nil {
		return tools.ToolResult{}, fatal("missing tool result", nil)
	}
	if result.IsError {
		return tools.ToolResult{}, &Error{Operation: "tool reported failure (server details withheld)"}
	}
	if result.NeedsInput() {
		return tools.ToolResult{}, &Error{Operation: "tool requires unsupported additional input; no retry performed"}
	}
	data, err := normalize(result)
	if err != nil {
		return tools.ToolResult{}, fatal("cannot normalize tool result", err)
	}
	return tools.ToolResult{Tool: call.Name, Data: data}, nil
}

// Structured content is authoritative and is never unwrapped or altered to fit
// a schema. Otherwise preserve a single JSON text block, or wrap all content
// blocks as JSON for non-JSON/multimodal results. Runtime still validates output.
func normalize(result *mcp.CallToolResult) (json.RawMessage, error) {
	if result.StructuredContent != nil {
		return json.Marshal(result.StructuredContent)
	}
	if len(result.Content) == 1 {
		if text, ok := result.Content[0].(*mcp.TextContent); ok && json.Valid([]byte(text.Text)) {
			return json.RawMessage(text.Text), nil
		}
	}
	data, err := json.Marshal(struct {
		Content []mcp.Content `json:"content"`
	}{result.Content})
	if err != nil {
		return nil, fmt.Errorf("encode content")
	}
	return data, nil
}
