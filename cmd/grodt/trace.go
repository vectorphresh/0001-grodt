package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	runstate "github.com/vectorphresh/0001-grodt/internal/state"
)

type inferenceTrace struct {
	dir   string
	store *runstate.Store
	key   string
}

func newInferenceTrace(parent string, store *runstate.Store, key string) (*inferenceTrace, error) {
	dir := filepath.Join(parent, store.Snapshot().Intrinsic.RunID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create trace directory: %w", err)
	}
	return &inferenceTrace{dir: dir, store: store, key: key}, nil
}

// Artifact failures are diagnostic only: a completed model/tool operation must
// not be reported as failed merely because a later trace write failed.
func (s *terminalStatus) saveArtifact(name string, value any) {
	if s == nil || s.artifacts == nil {
		return
	}
	t := s.artifacts
	data, err := json.MarshalIndent(value, "", "  ")
	if err == nil {
		if t.key != "" {
			encoded, _ := json.Marshal(t.key)
			data = []byte(strings.ReplaceAll(string(data), string(encoded[1:len(encoded)-1]), "[redacted]"))
		}
		err = os.WriteFile(filepath.Join(t.dir, name), append(data, '\n'), 0600)
	}
	if err != nil {
		_ = s.log("Trace artifact write failed: %s", name)
	}
}

func (c observedClient) traceRequest(operation string, request any) {
	if c.status == nil || c.status.artifacts == nil {
		return
	}
	data, _ := json.Marshal(request)
	state := c.status.artifacts.store.JSON()
	prefix := fmt.Sprintf("%06d", c.metrics.Requests)
	c.status.saveArtifact(prefix+"-request.json", map[string]any{
		"operation": operation, "timestamp": time.Now().UTC(),
		"cycle":         c.status.artifacts.store.Snapshot().Intrinsic.GlobalCycle,
		"request_bytes": len(data), "state_bytes": len(state), "request": request,
	})
	c.status.saveArtifact(prefix+"-state.json", state)
}

func (c observedClient) traceResponse(result any, start time.Time, err error) {
	message := ""
	if err != nil {
		message = "model request failed"
	}
	c.status.saveArtifact(fmt.Sprintf("%06d-response.json", c.metrics.Requests), map[string]any{
		"elapsed_ms": time.Since(start).Milliseconds(), "error": message, "response": result,
	})
}
