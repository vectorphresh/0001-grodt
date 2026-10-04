package mcp

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The standalone partition artifact mirrors the admission schema. Prevent drift
// without adding network schema resolution or coupling the WASM ABI to MCP SDK types.
func TestModuleResultSchemaMatchesAdmission(t *testing.T) {
	data, err := os.ReadFile("../../modules/mcp-tool-state/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var partition struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(data, &partition); err != nil {
		t.Fatal(err)
	}
	var actual, want any
	if err := json.Unmarshal(partition.Defs["mcpResult"], &actual); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(resultSchema, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatal("module partition MCP result schema differs from admission contract")
	}
}
