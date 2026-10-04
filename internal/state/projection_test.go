package state

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestModelProjectionExcludesAuditPayloads(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, nil, Options{RunID: "projection"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(ctx) })
	root, err := s.Push(ctx, "current objective", "original request payload")
	if err != nil {
		t.Fatal(err)
	}
	// Current knowledge is preserved exactly, including fixed baselines and
	// freshness metadata. Model projection never rewrites domain values.
	s.snapshot.Knowledge["world"] = Partition{Value: json.RawMessage(`{"baseline":100,"target":105,"equity":99}`), Metadata: Metadata{Version: 4, Stale: true}}
	before := s.ModelJSON()
	for i := 0; i < 30; i++ {
		ids, err := s.QueueAgentWork(ctx, []AgentWork{{OperationID: "op", Turn: 1, CallID: "call", Server: "server", Tool: "lookup", Arguments: json.RawMessage(`{"historical_argument":"private payload"}`)}})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ActivateAgentWork(ctx, ids[0]); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordAgentOutcome(ctx, json.RawMessage(`{"historical_result":"`+strings.Repeat("x", 4096)+`"}`), ""); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishAgentWork(ctx); err != nil {
			t.Fatal(err)
		}
	}
	after := s.ModelJSON()
	for _, payload := range []string{"historical_result", "historical_argument", "original request payload", "agent_work"} {
		if bytes.Contains(after, []byte(payload)) {
			t.Fatalf("projection leaked %s", payload)
		}
	}
	if len(after) > len(before)+10 {
		t.Fatalf("projection grew with history: %d -> %d", len(before), len(after))
	}
	var projected Snapshot
	if err := json.Unmarshal(after, &projected); err != nil {
		t.Fatal(err)
	}
	if len(projected.Tasks.Records) != 1 || projected.Tasks.Records[root].Status != "running" {
		t.Fatal("active task missing")
	}
	if !bytes.Equal(projected.Knowledge["world"].Value, s.snapshot.Knowledge["world"].Value) || !projected.Knowledge["world"].Metadata.Stale {
		t.Fatal("current knowledge or freshness lost")
	}
	if len(s.Snapshot().Tasks.Records) != 31 || !bytes.Contains(s.JSON(), []byte("historical_result")) {
		t.Fatal("task archive damaged")
	}
	if !bytes.Contains(encode(s.Journal()), []byte("historical_result")) {
		t.Fatal("journal provenance damaged")
	}
	ids, err := s.QueueAgentWork(ctx, []AgentWork{{OperationID: "op", Turn: 1, CallID: "pending", Server: "server", Tool: "lookup", Arguments: json.RawMessage(`{"pending_argument":"opaque"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	var pending struct {
		Tasks struct {
			Records map[string]struct{ Status, Server, Tool string }
		}
	}
	if err := json.Unmarshal(s.ModelJSON(), &pending); err != nil {
		t.Fatal(err)
	}
	if v := pending.Tasks.Records[ids[0]]; v.Status != "pending" || v.Server != "server" || v.Tool != "lookup" {
		t.Fatal("pending action identity missing")
	}
	if bytes.Contains(s.ModelJSON(), []byte("pending_argument")) {
		t.Fatal("pending provenance leaked")
	}
	if err := s.ActivateAgentWork(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(s.ModelJSON(), &projected); err != nil {
		t.Fatal(err)
	}
	if projected.Tasks.Records[root].Status != "waiting" || projected.Tasks.Records[ids[0]].Status != "running" {
		t.Fatal("active lineage missing")
	}
}
