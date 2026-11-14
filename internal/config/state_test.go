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

func TestStatePersistenceSelection(t *testing.T) {
	r := loadConfig(t, `state:
  definition: modules/gr33d-market-state/state.json
  persistence: state/gr33d-knowledge.json
  strategy_context_bytes: 4096
`)
	if r.State().Persistence != "state/gr33d-knowledge.json" {
		t.Fatal("persistence path not loaded")
	}
	if r.State().StrategyContextBytes != 4096 {
		t.Fatal("strategy context limit not loaded")
	}
	empty := loadConfig(t, `state:
  definition: modules/gr33d-market-state/state.json
`)
	if empty.State().Persistence != "" || empty.State().StrategyContextBytes != 0 {
		t.Fatal("implicit persistence configured")
	}
}
