package wasm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/validation"
)

const mcpModulePath = "../../../modules/mcp-tool-state/"

func mcpModule(t *testing.T) *Module {
	t.Helper()
	b, err := os.ReadFile(mcpModulePath + "mcp-tool-state.wasm")
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
func toolEvent(source, tool string, seq uint64, result string) state.Event {
	payload, _ := json.Marshal(struct {
		Tool   string          `json:"tool"`
		Result json.RawMessage `json:"result"`
	}{tool, json.RawMessage(result)})
	return state.Event{ID: fmt.Sprintf("run/event/%d", seq), Sequence: seq, Time: time.Unix(1, 0).UTC(), Source: state.Source{Kind: "mcp", ID: source}, Correlation: &state.Correlation{RequestID: "call", OperationID: "generation", Turn: 1}, Payload: payload}
}
func reduce(t *testing.T, m *Module, current json.RawMessage, e state.Event) (string, json.RawMessage) {
	t.Helper()
	out, err := m.Process(context.Background(), current, e)
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Status  string          `json:"status"`
		Replace json.RawMessage `json:"replace"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	if r.Status == "mutation" {
		schema, err := os.ReadFile(mcpModulePath + "schema.json")
		if err != nil {
			t.Fatal(err)
		}
		v, err := validation.New().Validate(context.Background(), schema, r.Replace)
		if err != nil || !v.Valid {
			t.Fatalf("invalid candidate: %s %v %s", out, err, v.Details)
		}
	}
	return r.Status, r.Replace
}

type cachedResult struct {
	Sequence    uint64             `json:"sequence"`
	Timestamp   time.Time          `json:"timestamp"`
	Correlation *state.Correlation `json:"correlation"`
	Result      json.RawMessage    `json:"result"`
}

func cache(t *testing.T, current json.RawMessage) map[string]map[string]cachedResult {
	t.Helper()
	var value struct {
		Sources map[string]map[string]cachedResult `json:"sources"`
	}
	if err := json.Unmarshal(current, &value); err != nil {
		t.Fatal(err)
	}
	return value.Sources
}
func compact(t *testing.T, b json.RawMessage) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := json.Compact(&out, b); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func TestMCPToolsOpaqueResults(t *testing.T) {
	m := mcpModule(t)
	for _, data := range []string{`{"position":"opaque","n":9007199254740993}`, `[1,{"x":null}]`, `"quotes \" and unicode ☃"`, `9007199254740993`, `1.2300e+100`, `true`, `false`, `null`, ""} {
		t.Run(data, func(t *testing.T) {
			result := `{"content":[{"type":"text","text":"{not parsed}"},{"type":"text","text":"second block"}],"isError":false`
			if data != "" {
				result += `,"structuredContent":` + data
			}
			result += `}`
			e := toolEvent("unfamiliar", "lookup", math.MaxUint64, result)
			current := json.RawMessage(`{"sources":{}}`)
			status, next := reduce(t, m, current, e)
			if status != "mutation" {
				t.Fatal(status)
			}
			entry := cache(t, next)["unfamiliar"]["lookup"]
			if entry.Sequence != math.MaxUint64 || !entry.Timestamp.Equal(e.Time) || entry.Correlation.RequestID != "call" {
				t.Fatal("metadata lost")
			}
			if !bytes.Equal(compact(t, entry.Result), compact(t, json.RawMessage(result))) {
				t.Fatalf("opaque result changed: %s", entry.Result)
			}
			_, again := reduce(t, m, current, e)
			if !bytes.Equal(next, again) {
				t.Fatal("non-deterministic reduction")
			}
		})
	}
}
func TestMCPToolsLatestPerSourceTool(t *testing.T) {
	m := mcpModule(t)
	current := json.RawMessage(`{"sources":{}}`)
	for _, e := range []state.Event{
		toolEvent("first", "lookup", 10, `{"content":[],"structuredContent":"first","isError":false}`),
		toolEvent("second", "lookup", 11, `{"content":[],"structuredContent":[2]}`),
		toolEvent("first", "other", 12, `{"content":[],"structuredContent":null}`),
		toolEvent("first", "lookup", 13, `{"content":[{"type":"text","text":"failed"}],"isError":true}`),
	} {
		status, next := reduce(t, m, current, e)
		if status != "mutation" {
			t.Fatal(status)
		}
		current = next
	}
	entries := cache(t, current)
	if len(entries) != 2 || len(entries["first"]) != 2 || entries["first"]["lookup"].Sequence != 13 || entries["second"]["lookup"].Sequence != 11 {
		t.Fatal("entries were conflated")
	}
	if !bytes.Contains(entries["first"]["lookup"].Result, []byte(`"isError":true`)) {
		t.Fatal("old success retained")
	}
	for _, seq := range []uint64{13, 12, 0} {
		status, next := reduce(t, m, current, toolEvent("first", "lookup", seq, `{"content":[]}`))
		if status != "ignored" || next != nil {
			t.Fatal("older/duplicate changed state")
		}
	}
	unrelated := toolEvent("first", "lookup", 99, `{"content":[]}`)
	unrelated.Source.Kind = "llm"
	status, _ := reduce(t, m, current, unrelated)
	if status != "ignored" {
		t.Fatal(status)
	}
}
func TestMCPToolsPropertyNamesAndEscaping(t *testing.T) {
	m := mcpModule(t)
	current := json.RawMessage(`{"sources":{}}`)
	names := []string{"a/b~c", "quotes\"\\\n\x00", "☃😀", "__proto__", "constructor", "<>&"}
	for i, name := range names {
		status, next := reduce(t, m, current, toolEvent(name, name, uint64(i+1), `{"content":[]}`))
		if status != "mutation" {
			t.Fatal(status)
		}
		current = next
	}
	for i, name := range names {
		status, next := reduce(t, m, current, toolEvent(name, name, uint64(i+100), `{"content":[],"isError":true}`))
		if status != "mutation" {
			t.Fatal(status)
		}
		current = next
	}
	entries := cache(t, current)
	if len(entries) != len(names) {
		t.Fatal("key collision")
	}
	for i, name := range names {
		if entries[name][name].Sequence != uint64(i+100) {
			t.Fatal("wrong key updated")
		}
	}
	// Lexically different but equivalent JSON strings identify the same entry.
	current = json.RawMessage(`{"sources":{"\u0077eather":{"\uD83D\uDE00":{"sequence":1,"timestamp":"t","result":{"content":[]}}}}}`)
	status, next := reduce(t, m, current, toolEvent("weather", "😀", 2, `{"content":[]}`))
	if status != "mutation" {
		t.Fatal(status)
	}
	var v struct {
		Sources map[string]map[string]json.RawMessage `json:"sources"`
	}
	_ = json.Unmarshal(next, &v)
	if len(v.Sources) != 1 || len(v.Sources["weather"]) != 1 {
		t.Fatal("escaped name duplicated")
	}
}
func TestMCPToolsProcessingErrors(t *testing.T) {
	m := mcpModule(t)
	valid := toolEvent("source", "tool", 1, `{"content":[]}`)
	for _, current := range []string{`null`, `{}`, `{"sources":[]}`, `{"sources":{"source":3}}`, `{"sources":{"source":{"tool":{"sequence":-1,"timestamp":"t","result":{"content":[]}}}}}`} {
		status, _ := reduce(t, m, json.RawMessage(current), valid)
		if status != "error" {
			t.Fatalf("invalid state accepted: %s", current)
		}
	}
	for _, payload := range []string{`null`, `{}`, `{"tool":"","result":{"content":[]}}`, `{"tool":"tool","result":null}`, `{"tool":"tool","result":{"content":{},"isError":false}}`, `{"tool":"tool","result":{"content":[],"isError":"false"}}`} {
		e := valid
		e.Payload = json.RawMessage(payload)
		status, _ := reduce(t, m, json.RawMessage(`{"sources":{}}`), e)
		if status != "error" {
			t.Fatalf("invalid event accepted: %s", payload)
		}
	}
}
func TestMCPToolsPartitionKeepsEstablishedStalenessSemantics(t *testing.T) {
	ctx := context.Background()
	store, err := Load(ctx, mcpModulePath+"state.json", state.Options{RunID: "cache"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(ctx)
	// Bypasses MCP admission deliberately to exercise a module contract failure.
	if _, err := store.Admit(ctx, state.Source{Kind: "mcp", ID: "first"}, json.RawMessage(`{"tool":"lookup","result":null}`)); err != nil {
		t.Fatal(err)
	}
	if !store.Snapshot().Knowledge["mcp_tools"].Metadata.Stale {
		t.Fatal("failure did not mark stale")
	}
	e := toolEvent("second", "lookup", 0, `{"content":[],"isError":true}`)
	admitted, err := store.Admit(ctx, e.Source, e.Payload)
	if err != nil {
		t.Fatal(err)
	}
	partition := store.Snapshot().Knowledge["mcp_tools"]
	if partition.Metadata.Stale || partition.Metadata.LastProcessedSequence != admitted.Sequence {
		t.Fatal("base-state behavior changed")
	}
	// This clearing says nothing about recovery/freshness of first/lookup.
	if _, ok := cache(t, partition.Value)["first"]; ok {
		t.Fatal("invented failed source recovery")
	}
	// A retained entry ahead of host sequence is only a synthetic fixture for
	// verifying that an older result cannot clear staleness.
	binary, _ := os.ReadFile(mcpModulePath + "mcp-tool-state.wasm")
	module, err := New(ctx, binary)
	if err != nil {
		t.Fatal(err)
	}
	schema, _ := os.ReadFile(mcpModulePath + "schema.json")
	initial := json.RawMessage(`{"sources":{"first":{"lookup":{"sequence":100,"timestamp":"t","result":{"content":[]}}}}}`)
	second, err := state.New(ctx, []state.Definition{{Name: "cache", Schema: schema, Initial: initial, Module: module}}, state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(ctx)
	second.Admit(ctx, state.Source{Kind: "mcp", ID: "first"}, json.RawMessage(`{"tool":"lookup","result":null}`))
	second.Admit(ctx, state.Source{Kind: "mcp", ID: "first"}, json.RawMessage(`{"tool":"lookup","result":{"content":[]}}`))
	if !second.Snapshot().Knowledge["cache"].Metadata.Stale {
		t.Fatal("ignored cleared staleness")
	}
}
