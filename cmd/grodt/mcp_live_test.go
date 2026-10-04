//go:build live

package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/config"
)

// Explicit final acceptance with a configured LLM and a harmless local MCP server.
// go test -tags live ./cmd/grodt -run '^TestLiveMCPFeedbackLoop$' -count=1 -v
func TestLiveMCPFeedbackLoop(t *testing.T) {
	path := os.Getenv("GRODT_LIVE_CONFIG")
	if path == "" {
		path = "../../config.yaml"
	}
	client, key, err := configuredClient(path)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, config.ErrNotFound) {
		t.Skip("live provider configuration unavailable; MCP milestone not evaluated")
	}
	if err != nil {
		t.Fatal("live provider configuration invalid")
	}
	h := newMCPMilestone(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err = h.run(ctx, client, key)
	t.Log(h.diagnostics.String())
	if err != nil {
		t.Fatalf("live MCP milestone failed: %v", err)
	}
	t.Logf("verified MCP→observation→WASM→commit→LLM; outer_cycles=%d MCP_invocations=%d", h.store.Snapshot().Intrinsic.GlobalCycle, h.runtime.Invocations())
}
