package loop

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

const catalogPageSize = 127
const selectToolPage = "grodt_select_tool_page"

func catalogPage(tools []toolcall.Definition, page int, pinned ...toolcall.Definition) ([]toolcall.Definition, string) {
	if len(tools)+len(pinned) <= 128 {
		return append(append([]toolcall.Definition(nil), tools...), pinned...), ""
	}
	pageSize := catalogPageSize - len(pinned)
	pages := (len(tools) + pageSize - 1) / pageSize
	start := page * pageSize
	end := min(start+pageSize, len(tools))
	out := append([]toolcall.Definition(nil), tools[start:end]...)
	schema := fmt.Sprintf(`{"type":"object","properties":{"page":{"type":"integer","minimum":0,"maximum":%d}},"required":["page"],"additionalProperties":false}`, pages-1)
	out = append(out, pinned...)
	out = append(out, toolcall.Definition{Name: selectToolPage, Description: "Select a tool catalog page for the next inference. Call alone; selection executes no external tools. All pages remain available.", InputSchema: json.RawMessage(schema)})
	var index strings.Builder
	fmt.Fprintf(&index, "\nTool catalog: %d tools on %d pages; current page %d. Use %s alone to switch pages. Page index (data):\n", len(tools), pages, page, selectToolPage)
	for i, t := range tools {
		label := strings.SplitN(t.Description, ":", 2)[0]
		fmt.Fprintf(&index, "page %d: %s %s\n", i/pageSize, t.Name, label)
	}
	for _, t := range pinned {
		fmt.Fprintf(&index, "all pages: %s %s\n", t.Name, t.Description)
	}
	return out, index.String()
}

func selectedPage(calls []toolcall.Call, total int, reserved ...int) (int, bool, error) {
	pageSize := catalogPageSize
	if len(reserved) > 0 {
		pageSize -= reserved[0]
	}
	for _, c := range calls {
		if c.Name != selectToolPage {
			continue
		}
		if len(calls) != 1 || c.ID == "" {
			return 0, true, fmt.Errorf("call the catalog page selector alone with a nonempty call ID")
		}
		var fields map[string]json.RawMessage
		var page int
		if json.Unmarshal(c.Arguments, &fields) != nil || len(fields) != 1 || json.Unmarshal(fields["page"], &page) != nil || page < 0 || page >= (total+pageSize-1)/pageSize {
			return 0, true, fmt.Errorf("select an integer page within the catalog range")
		}
		return page, true, nil
	}
	return 0, false, nil
}
