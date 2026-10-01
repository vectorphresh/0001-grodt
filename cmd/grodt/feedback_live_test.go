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

// Explicit milestone acceptance; no LLM access is included in ordinary tests.
// go test -tags live ./cmd/grodt -run '^TestLiveStatefulFeedbackLoop$' -count=1 -v
func TestLiveStatefulFeedbackLoop(t *testing.T) {
	path := os.Getenv("GRODT_LIVE_CONFIG")
	if path == "" {
		path = "../../config.yaml"
	}
	client, key, err := configuredClient(path)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, config.ErrNotFound) {
		t.Skip("live provider configuration is unavailable; milestone not evaluated")
	}
	if err != nil {
		t.Fatal("live provider configuration is invalid")
	}
	h := newFeedbackHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err = h.run(ctx, client, key)
	t.Log(h.diagnostics.String())
	if err != nil {
		t.Fatalf("live feedback milestone did not complete: %v", err)
	}
	assertFeedbackMilestone(t, h)
	t.Log("goal=satisfied; discovery committed before the later move decision; authoritative state matches environment truth")
}
