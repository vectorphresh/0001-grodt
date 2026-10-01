package wasm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/state"
)

func load(t *testing.T, name string) *Module {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".wasm")
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close(context.Background()) })
	return m
}
func event(action string) state.Event {
	return state.Event{ID: "run/event/1", Sequence: 1, Time: time.Unix(0, 0).UTC(), Source: state.Source{Kind: "runtime", ID: "fixture"}, Payload: json.RawMessage(fmt.Sprintf(`{"action":%q}`, action))}
}
func TestReferenceDeterminism(t *testing.T) {
	m := load(t, "reference")
	for i := 0; i < 3; i++ {
		b, err := m.Process(context.Background(), json.RawMessage(`10`), event("increment"))
		if err != nil || string(b) != `{"status":"mutation","replace":11}` {
			t.Fatalf("%s %v", b, err)
		}
	}
	for _, status := range []string{"processed", "error", "ignored"} {
		b, err := m.Process(context.Background(), json.RawMessage(`10`), event(status))
		if err != nil || string(b) != fmt.Sprintf(`{"status":%q}`, status) {
			t.Fatalf("%s %v", b, err)
		}
	}
}
func TestInitialCompliance(t *testing.T) {
	for _, name := range []string{"fault-1", "fault-7"} {
		b, _ := os.ReadFile("testdata/" + name + ".wasm")
		if m, err := New(context.Background(), b); err == nil {
			m.Close(context.Background())
			t.Fatal("bad ABI/import accepted")
		}
	}
	if m, err := New(context.Background(), []byte("invalid")); err == nil {
		m.Close(context.Background())
		t.Fatal("invalid binary accepted")
	}
	if m, err := New(context.Background(), make([]byte, MaxModuleBytes+1)); err == nil {
		m.Close(context.Background())
		t.Fatal("oversized binary accepted")
	}
}
func TestRuntimeLimits(t *testing.T) {
	for _, tt := range []struct{ name, code string }{{"fault-2", "trap"}, {"fault-3", "timeout"}, {"fault-4", "malformed_response"}, {"fault-5", "resource_limit"}, {"fault-6", "malformed_response"}} {
		t.Run(tt.name, func(t *testing.T) {
			m := load(t, tt.name)
			start := time.Now()
			_, err := m.Process(context.Background(), json.RawMessage(`0`), event("increment"))
			var e *state.RuntimeError
			if !errors.As(err, &e) || e.Code != tt.code {
				t.Fatalf("%v", err)
			}
			if time.Since(start) > 5*time.Second {
				t.Fatal("execution unbounded")
			}
		})
	}
	m := load(t, "fault-8")
	b, err := m.Process(context.Background(), json.RawMessage(`0`), event("increment"))
	if err != nil || string(b) != `{"status":"processed"}` {
		t.Fatalf("memory growth not bounded: %s %v", b, err)
	}
	m = load(t, "fault-3")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := m.Process(ctx, json.RawMessage(`0`), event("increment")); err == nil || time.Since(start) > time.Second {
		t.Fatal("cancellation ignored")
	}
}
func TestDefinitionInitialization(t *testing.T) {
	dir := t.TempDir()
	b, _ := os.ReadFile("testdata/reference.wasm")
	os.WriteFile(filepath.Join(dir, "m.wasm"), b, 0600)
	os.WriteFile(filepath.Join(dir, "schema.json"), []byte(`{"type":"integer"}`), 0600)
	os.WriteFile(filepath.Join(dir, "initial.json"), []byte(`0`), 0600)
	manifest := `{"partitions":[{"name":"counter","schema":"schema.json","initial":"initial.json","module":"m.wasm","abi":"grodt.state/v1"}]}`
	path := filepath.Join(dir, "state.json")
	os.WriteFile(path, []byte(manifest), 0600)
	s, err := Load(context.Background(), path, state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if _, err := s.Admit(context.Background(), state.Source{Kind: "runtime", ID: "test"}, json.RawMessage(`{"action":"increment"}`)); err != nil {
		t.Fatal(err)
	}
	if string(s.Snapshot().Knowledge["counter"].Value) != "1" {
		t.Fatal("reference module not wired")
	}
	bad, _ := os.ReadFile("testdata/fault-1.wasm")
	os.WriteFile(filepath.Join(dir, "m.wasm"), bad, 0600)
	if s, err := Load(context.Background(), path, state.Options{}); err == nil {
		s.Close(context.Background())
		t.Fatal("ABI failure did not abort initialization")
	}
	os.WriteFile(filepath.Join(dir, "m.wasm"), b, 0600)
	os.WriteFile(filepath.Join(dir, "initial.json"), []byte(`"bad"`), 0600)
	if s, err := Load(context.Background(), path, state.Options{}); err == nil {
		s.Close(context.Background())
		t.Fatal("bad default accepted")
	}
}

func TestMissingExportsAndMalformedEnvelope(t *testing.T) {
	binary, err := os.ReadFile("testdata/reference.wasm")
	if err != nil {
		t.Fatal(err)
	}
	for _, names := range [][2]string{{"grodt_process", "other_process"}, {"memory", "memori"}} {
		malformed := bytes.ReplaceAll(binary, []byte(names[0]), []byte(names[1]))
		if m, err := New(context.Background(), malformed); err == nil {
			m.Close(context.Background())
			t.Fatal("missing export accepted")
		}
	}
	m := load(t, "fault-9")
	s, err := state.New(context.Background(), []state.Definition{{Name: "p", Schema: json.RawMessage(`true`), Initial: json.RawMessage(`0`), Module: m}}, state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if _, err := s.Admit(context.Background(), state.Source{Kind: "runtime", ID: "fixture"}, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	p := s.Snapshot().Knowledge["p"]
	if !p.Metadata.Stale || p.Metadata.LastError.Code != "malformed_response" || string(p.Value) != "0" {
		t.Fatal("malformed envelope accepted")
	}
}

func TestDefinitionShapeAndInputBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	for _, data := range []string{`null`, `{}`, `{"partitions":null}`, `{"partitions":[],"unknown":true}`, `{"partitions":[]} {}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if s, err := Load(context.Background(), path, state.Options{}); err == nil {
			s.Close(context.Background())
			t.Fatalf("accepted definition %s", data)
		}
	}
	if err := os.WriteFile(path, []byte(`{"partitions":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(context.Background(), path, state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.Close(context.Background())
	m := load(t, "reference")
	huge := json.RawMessage(`"` + strings.Repeat("x", state.MaxInputBytes) + `"`)
	_, err = m.Process(context.Background(), huge, event("increment"))
	var runtimeErr *state.RuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Code != "resource_limit" {
		t.Fatal("oversized combined input accepted")
	}
}

func TestReferenceHostRequestExtension(t *testing.T) {
	m := load(t, "reference")
	s, err := state.New(context.Background(), []state.Definition{{Name: "p", Schema: json.RawMessage(`{"type":"integer"}`), Initial: json.RawMessage(`0`), Module: m}}, state.Options{RunID: "reference"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	root, err := s.Push(context.Background(), "root", "input")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Admit(context.Background(), state.Source{Kind: "runtime", ID: "fixture"}, json.RawMessage(`{"action":"request"}`))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := s.Snapshot()
	child := snapshot.Tasks.Records["reference/task/2"]
	if child.ParentID != root || child.Work == nil || child.Work.Request.ID != "reference" || child.Error != "http_disabled" || child.Status != "failed" {
		t.Fatalf("child: %+v", child)
	}
	if snapshot.Knowledge["p"].Metadata.Stale || snapshot.Intrinsic.EventSequence != 2 || snapshot.Intrinsic.HostExecutions != 1 {
		t.Fatal("ABI extension did not use normal host/event path")
	}
}
