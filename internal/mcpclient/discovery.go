package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vectorphresh/0001-grodt/internal/tools"
)

// Discover caches a complete discovery snapshot; registry owns public names.
func (p *Provider) Discover(ctx context.Context) ([]tools.ToolDefinition, error) {
	call, finish, err := p.callContext(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	p.discoveryMu.Lock()
	defer p.discoveryMu.Unlock()
	if err := call.Err(); err != nil {
		return nil, err
	}
	if p.discovered {
		return cloneDefinitions(p.definitions), nil
	}
	var definitions []tools.ToolDefinition
	names := map[string]bool{}
	cursors := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages >= 1000 {
			return nil, fatal("tool discovery exceeded page limit", nil)
		}
		page, err := p.session.ListTools(call, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			if call.Err() != nil {
				return nil, call.Err()
			}
			return nil, fatal("tool discovery failed", err)
		}
		if page == nil {
			return nil, fatal("missing tool discovery result", nil)
		}
		for _, remote := range page.Tools {
			if remote == nil || strings.TrimSpace(remote.Name) == "" || remote.InputSchema == nil {
				return nil, fatal("malformed tool definition", nil)
			}
			if names[remote.Name] {
				return nil, fmt.Errorf("MCP discovery contains duplicate tool names")
			}
			names[remote.Name] = true
			input, err := json.Marshal(remote.InputSchema)
			if err != nil {
				return nil, fatal("input schema encoding failed", err)
			}
			var output json.RawMessage
			if remote.OutputSchema != nil {
				output, err = json.Marshal(remote.OutputSchema)
				if err != nil {
					return nil, fatal("output schema encoding failed", err)
				}
			}
			definitions = append(definitions, tools.ToolDefinition{Name: remote.Name, Description: remote.Description, InputSchema: input, OutputSchema: output, Source: p.name})
		}
		if page.NextCursor == "" {
			break
		}
		if cursors[page.NextCursor] {
			return nil, fatal("tool discovery repeated a cursor", nil)
		}
		cursors[page.NextCursor] = true
		cursor = page.NextCursor
	}
	p.definitions = definitions
	p.discovered = true
	return cloneDefinitions(definitions), nil
}

func cloneDefinitions(definitions []tools.ToolDefinition) []tools.ToolDefinition {
	copy := append([]tools.ToolDefinition(nil), definitions...)
	for i := range copy {
		copy[i].InputSchema = append([]byte(nil), copy[i].InputSchema...)
		copy[i].OutputSchema = append([]byte(nil), copy[i].OutputSchema...)
	}
	return copy
}
