package config

import "testing"

func TestStateDefinitionSelection(t *testing.T) {
	r := loadConfig(t, `state:
  definition: modules/mcp-tool-state/state.json
openai:
  environment:
    OPENAI_API_KEY: test-key
`)
	if r.State().Definition != "modules/mcp-tool-state/state.json" {
		t.Fatal("state definition not loaded")
	}
	assertValue(t, r, "openai", "OPENAI_API_KEY", "test-key")
	if loadConfig(t, "{}").State().Definition != "" {
		t.Fatal("implicit state module selected")
	}
	var empty Resolver
	if empty.State().Definition != "" {
		t.Fatal("zero resolver selected state")
	}
}
