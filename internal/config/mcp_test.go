package config

import "testing"

func TestMCPConfigIsIndependentImmutableSnapshot(t *testing.T) {
	t.Setenv("ONLY_OS", "must-not-copy")
	r := loadConfig(t, `openai:
  environment:
    OPENAI_API_KEY: existing-key
mcp:
  servers:
    - name: unfamiliar
      transport: http
      url: https://example.invalid/mcp
      environment:
        TOKEN: opaque-token
        LOCAL_OPTION: stays-local
      http:
        headers:
          Authorization:
            from_environment: TOKEN
            prefix: "Bearer "
`)
	cfg := r.MCP()
	if len(cfg.Servers) != 1 {
		t.Fatal("missing MCP")
	}
	s := cfg.Servers[0]
	if s.HTTP.Headers["Authorization"].FromEnvironment != "TOKEN" || len(s.Environment) != 2 {
		t.Fatal("wrong bindings")
	}
	cfg.Servers[0].Environment["TOKEN"] = "modified"
	delete(cfg.Servers[0].HTTP.Headers, "Authorization")
	if r.MCP().Servers[0].Environment["TOKEN"] != "opaque-token" || len(r.MCP().Servers[0].HTTP.Headers) != 1 {
		t.Fatal("mutable resolver")
	}
	assertValue(t, r, "openai", "OPENAI_API_KEY", "existing-key")
}
