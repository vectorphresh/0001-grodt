package state

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestKnowledgeProjectionFairnessAndExactEvidence(t *testing.T) {
	large := []any{}
	for i := 0; i < 400; i++ {
		large = append(large, map[string]any{"symbol": fmt.Sprintf("item-%d", i), "data": strings.Repeat("x", 200)})
	}
	sources := map[string]any{"server": map[string]any{
		"large":  map[string]any{"result": map[string]any{"structuredContent": large}},
		"orders": map[string]any{"result": map[string]any{"structuredContent": map[string]any{"orders": []any{}}}},
		"quotes": map[string]any{"result": map[string]any{"structuredContent": map[string]any{"price": 42}}},
	}}
	s := newStore(t, Definition{Name: "mcp_tools", Schema: json.RawMessage(`{"type":"object"}`), Initial: encode(map[string]any{"sources": sources}), Module: &processor{}}, Definition{Name: "capacity", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{"capacity":100}`), Module: &processor{}})
	before := s.Snapshot()
	projection := s.ProjectKnowledge(before, nil, 12<<10, 64, 4<<10)
	if len(encode(projection)) > 12<<10 || !projection.Truncated {
		t.Fatal("size or partial coverage missing")
	}
	seen := map[string]bool{}
	for _, entry := range projection.Evidence {
		value, err := s.ResolveEvidence(entry.Reference)
		if err != nil || !bytes.Equal(value, entry.Value) {
			t.Fatalf("non-authoritative projected value: %v", err)
		}
		if entry.Recent {
			t.Fatal("unchanged state labeled recent")
		}
		seen[entry.Reference.Partition+entry.Reference.Path] = true
	}
	for _, coverage := range projection.Sources {
		if coverage.Selected == 0 {
			t.Fatalf("source crowded out: %+v", coverage)
		}
		if strings.HasSuffix(coverage.Reference.Path, "/large") && (coverage.Coverage != "partial" || coverage.Omitted == 0) {
			t.Fatal("large result presented as exhaustive")
		}
	}
	if len(projection.Sources) != 4 {
		t.Fatal("partitions or MCP sources absent")
	}
	if !bytes.Equal(encode(projection), encode(s.ProjectKnowledge(before, nil, 12<<10, 64, 4<<10))) {
		t.Fatal("projection is nondeterministic")
	}
	for i, a := range projection.Evidence {
		for _, b := range projection.Evidence[i+1:] {
			if a.Reference.Partition == b.Reference.Partition && (a.Reference.Path == b.Reference.Path || strings.HasPrefix(a.Reference.Path, b.Reference.Path+"/") || strings.HasPrefix(b.Reference.Path, a.Reference.Path+"/")) {
				t.Fatal("overlapping values duplicated")
			}
		}
	}
}

func TestKnowledgeProjectionDuplicateTextAndDistinctText(t *testing.T) {
	for _, distinct := range []bool{false, true} {
		t.Run(fmt.Sprint(distinct), func(t *testing.T) {
			payload := map[string]any{"unique_marker": "canonical_observation"}
			text := string(encode(payload))
			if distinct {
				text = "Distinct tool explanation"
			}
			initial := encode(map[string]any{"sources": map[string]any{"server": map[string]any{"read": map[string]any{"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "structuredContent": payload}}}}})
			s := newStore(t, Definition{Name: "mcp_tools", Schema: json.RawMessage(`{"type":"object"}`), Initial: initial, Module: &processor{}})
			projection := s.ProjectKnowledge(s.Snapshot(), []EvidenceReference{{Partition: "mcp_tools", Path: "/sources/server/read"}}, 16<<10, 64, 4<<10)
			if projection.Sources[0].Deduplicated == distinct {
				t.Fatal("incorrect duplicate detection")
			}
			wire := encode(projection.Evidence)
			if !distinct && bytes.Count(wire, []byte("canonical_observation")) != 1 {
				t.Fatal("same payload included twice")
			}
			if distinct && !bytes.Contains(wire, []byte("Distinct tool explanation")) {
				t.Fatal("distinct text lost")
			}
		})
	}
}

func TestKnowledgeProjectionOversizedTextAndSourceCardinality(t *testing.T) {
	tools := map[string]any{"a_huge": map[string]any{"text": strings.Repeat("x", 64<<10)}}
	for i := 0; i < 120; i++ {
		tools[fmt.Sprintf("source_%03d", i)] = map[string]any{"value": i}
	}
	s := newStore(t, Definition{Name: "mcp_tools", Schema: json.RawMessage(`{"type":"object"}`), Initial: encode(map[string]any{"sources": map[string]any{"s": tools}}), Module: &processor{}}, Definition{Name: "other", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{"value":true}`), Module: &processor{}})
	projection := s.ProjectKnowledge(s.Snapshot(), nil, 8<<10, 64, 4096)
	if len(encode(projection)) > 8<<10 || projection.OmittedSources == 0 || !projection.Truncated {
		t.Fatal("inventory overflow unbounded or unreported")
	}
	other := false
	for _, c := range projection.Sources {
		if c.Reference.Partition == "other" {
			other = true
		}
		if strings.HasSuffix(c.Reference.Path, "a_huge") && c.Coverage != "partial" {
			t.Fatal("oversized string considered inspected")
		}
	}
	if !other {
		t.Fatal("source cardinality crowded out another partition")
	}
}

func TestKnowledgeProjectionPreferredPointerStaleAndFreshness(t *testing.T) {
	m := &processor{fn: func(_ json.RawMessage, e Event) (json.RawMessage, error) {
		if e.Source.ID == "stale" {
			return json.RawMessage(`{"status":"error"}`), nil
		}
		return json.RawMessage(`{"status":"mutation","replace":{"earlier":true,"current":42}}`), nil
	}}
	s := newStore(t, Definition{Name: "world", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{"earlier":true}`), Module: m})
	before := s.Snapshot()
	if _, err := s.Admit(context.Background(), Source{Kind: "mcp", ID: "current"}, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	projection := s.ProjectKnowledge(before, []EvidenceReference{{Partition: "world", Path: "/earlier"}}, 4096, 64, 4096)
	if len(projection.Evidence) < 2 || projection.Evidence[0].Reference.Path != "/earlier" || projection.Evidence[0].Recent {
		t.Fatal("earlier unchanged preferred observation absent or mislabeled")
	}
	if !projection.Evidence[1].Recent {
		t.Fatal("new evidence unlabeled")
	}
	if _, err := s.Admit(context.Background(), Source{Kind: "mcp", ID: "stale"}, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	projection = s.ProjectKnowledge(before, nil, 4096, 64, 4096)
	if len(projection.Evidence) != 0 || projection.Sources[0].Freshness != "stale" || !projection.Truncated {
		t.Fatal("stale values available for new satisfaction")
	}
}

func TestKnowledgeProjectionCustomSourcesPrecisionAndEscaping(t *testing.T) {
	s := newStore(t, Definition{Name: "custom_tools", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{"sources":{"server":{"one":{"a/b":{"~number":123456789012345678901234567890}},"two":{"value":null}}}}`), Module: &processor{}})
	projection := s.ProjectKnowledge(Snapshot{}, []EvidenceReference{{Partition: "custom_tools", Path: "/sources/server/one/a~1b/~0number"}}, 8<<10, 64, 4096)
	if len(projection.Sources) != 2 {
		t.Fatal("custom MCP partition sources not fairly inventoried")
	}
	found := false
	for _, entry := range projection.Evidence {
		if !entry.Recent {
			t.Fatal("previously absent value not labeled recent")
		}
		if entry.Reference.Path == "/sources/server/one/a~1b/~0number" {
			found = true
			if string(entry.Value) != "123456789012345678901234567890" {
				t.Fatal("number rounded")
			}
		}
		resolved, err := s.ResolveEvidence(entry.Reference)
		if err != nil || !bytes.Equal(resolved, entry.Value) {
			t.Fatal("escaped pointer does not resolve")
		}
	}
	if !found {
		t.Fatal("preferred escaped observation missing")
	}
}

func TestKnowledgeProjectionMixedContentDeduplicatesOnlyMatchingBlock(t *testing.T) {
	payload := map[string]any{"marker": "single_canonical_payload"}
	content := []any{map[string]any{"type": "text", "text": string(encode(payload))}, map[string]any{"type": "text", "text": "Distinct explanatory text"}}
	s := newStore(t, Definition{Name: "mcp_tools", Schema: json.RawMessage(`{"type":"object"}`), Initial: encode(map[string]any{"sources": map[string]any{"server": map[string]any{"read": map[string]any{"result": map[string]any{"content": content, "structuredContent": payload}}}}}), Module: &processor{}})
	projection := s.ProjectKnowledge(s.Snapshot(), nil, 16<<10, 64, 4096)
	wire := encode(projection.Evidence)
	if bytes.Count(wire, []byte("single_canonical_payload")) != 1 || !bytes.Contains(wire, []byte("Distinct explanatory text")) || !projection.Sources[0].Deduplicated {
		t.Fatal("mixed content duplicated data or lost distinct text")
	}
}
