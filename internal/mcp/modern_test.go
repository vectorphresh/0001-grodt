package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vectorphresh/0001-grodt/internal/config"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

func TestOfficialSDKModernStreamableServer(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "unfamiliar-modern", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "independent", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "modern protocol"}}, StructuredContent: json.RawMessage(`false`)}, nil
	})
	endpoint := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil))
	defer endpoint.Close()
	runtime, err := New(context.Background(), config.MCPConfig{Servers: []config.MCPServer{{Name: "modern", Transport: "http", URL: endpoint.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	result, err := runtime.Invoke(context.Background(), toolcall.Call{ID: "call", Name: runtime.Tools()[0].Name, Arguments: json.RawMessage(`{}`)})
	if err != nil || string(result.StructuredContent) != "false" {
		t.Fatal(result, err)
	}
}
