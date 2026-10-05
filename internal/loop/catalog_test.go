package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/vectorphresh/0001-grodt/internal/config"
	"github.com/vectorphresh/0001-grodt/internal/mcp"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCatalogPagingContinuation(t *testing.T) {
	p, f := toolsHarness(t, nil, false)
	tools := make([]map[string]any, 205)
	for i := range tools {
		tools[i] = map[string]any{"name": fmt.Sprintf("tool_%d", i), "inputSchema": map[string]any{"type": "object"}}
	}
	raw, _ := json.Marshal(tools)
	f.Tools = raw
	server := httptest.NewServer(f)
	defer server.Close()
	r, err := mcp.New(context.Background(), config.MCPConfig{Servers: []config.MCPServer{{Name: "all", Transport: "http", URL: server.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	p.Runtime = r
	requests := 0
	p.Client = nativeClient(func(_ context.Context, request toolcall.Request) (toolcall.Response, error) {
		requests++
		pinned := false
		for _, def := range request.Tools {
			if def.Name == planningTool {
				pinned = true
			}
		}
		if !pinned {
			t.Fatal("planning tool inaccessible on active page")
		}
		if len(request.Tools) > 128 {
			t.Fatal("provider tool limit exceeded")
		}
		if !strings.Contains(request.Instructions, "page 1: grodt_tool_144") {
			t.Fatal("undisplayed tools not discoverable")
		}
		switch requests {
		case 1:
			if len(request.Tools) != 128 {
				t.Fatal("incorrect first page")
			}
			return toolcall.Response{Calls: []toolcall.Call{{ID: "select", Name: selectToolPage, Arguments: json.RawMessage(`{"page":1}`)}}}, nil
		case 2:
			if f.Calls.Load() != 0 || request.Messages[2].CallID != "select" {
				t.Fatal("page selection executed external work or lost result")
			}
			if len(request.Tools) != 81 || request.Tools[0].Name != "grodt_tool_127" {
				t.Fatal("wrong second page")
			}
			return toolcall.Response{Calls: []toolcall.Call{{ID: "invoke", Name: "grodt_tool_144", Arguments: json.RawMessage(`{}`)}}}, nil
		default:
			return toolcall.Response{Text: "done"}, nil
		}
	})
	if _, err := p.Handle(context.Background(), &State{Objective: "goal", Prompt: "request"}); err != nil {
		t.Fatal(err)
	}
	if requests != 3 || f.Calls.Load() != 1 {
		t.Fatal("hidden tool not executed exactly once")
	}
}

func TestCatalogSmallAndInvalidSelections(t *testing.T) {
	for _, size := range []int{0, 1, 128, 129, 205, 256} {
		tools := make([]toolcall.Definition, size)
		pages := 1
		if size > 128 {
			pages = (size + 126) / 127
		}
		seen := 0
		for page := 0; page < pages; page++ {
			defs, _ := catalogPage(tools, page)
			if len(defs) > 128 {
				t.Fatal("limit exceeded")
			}
			if size > 128 {
				seen += len(defs) - 1
			} else {
				seen += len(defs)
			}
		}
		if seen != size {
			t.Fatal("catalog truncated")
		}
	}
	for _, args := range []string{`{"page":-1}`, `{"page":2}`, `{"page":0.5}`, `{}`, `{"page":0,"extra":true}`} {
		if _, _, err := selectedPage([]toolcall.Call{{ID: "x", Name: selectToolPage, Arguments: json.RawMessage(args)}}, 205); err == nil {
			t.Fatal("bad selection accepted")
		}
	}
}

func TestPlanningToolPinnedAcrossCatalogBoundaries(t *testing.T) {
	for _, size := range []int{0, 127, 128, 129, 205, 253, 254, 256} {
		tools := make([]toolcall.Definition, size)
		for i := range tools {
			tools[i].Name = fmt.Sprintf("tool_%d", i)
		}
		pages := 1
		if size+1 > 128 {
			pages = (size + 125) / 126
		}
		seen := map[string]bool{}
		for page := 0; page < pages; page++ {
			defs, index := catalogPage(tools, page, planDefinition())
			if len(defs) > 128 {
				t.Fatal("provider limit exceeded")
			}
			planning, selector := 0, 0
			for _, def := range defs {
				switch def.Name {
				case planningTool:
					planning++
				case selectToolPage:
					selector++
				default:
					if seen[def.Name] {
						t.Fatal("external tool repeated across pages")
					}
					seen[def.Name] = true
				}
			}
			if planning != 1 {
				t.Fatal("planning tool hidden by catalog navigation")
			}
			if pages > 1 && (selector != 1 || !strings.Contains(index, "all pages: "+planningTool)) {
				t.Fatal("navigation or pinned-tool index missing")
			}
			if size+1 > 128 {
				raw := json.RawMessage(fmt.Sprintf(`{"page":%d}`, page))
				selected, handled, err := selectedPage([]toolcall.Call{{ID: "page", Name: selectToolPage, Arguments: raw}}, size, 1)
				if !handled || err != nil || selected != page {
					t.Fatal("page boundary validation disagrees with rendered catalog", err)
				}
			}
		}
		if len(seen) != size {
			t.Fatal("external tools lost during reserved-slot paging")
		}
	}
}
