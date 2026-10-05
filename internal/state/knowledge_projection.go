package state

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// EvidenceValue is an exact subtree of accepted knowledge, never an LLM summary.
type EvidenceValue struct {
	Reference EvidenceReference `json:"reference"`
	Value     json.RawMessage   `json:"value,omitempty"`
	Freshness string            `json:"freshness"`
	Recent    bool              `json:"recent,omitempty"`
}

type SourceCoverage struct {
	Reference    EvidenceReference `json:"reference"`
	Freshness    string            `json:"freshness"`
	Coverage     string            `json:"coverage"`
	Selected     int               `json:"selected_values"`
	Omitted      int               `json:"omitted_values"`
	Deduplicated bool              `json:"duplicate_text_omitted,omitempty"`
	Type         string            `json:"value_type"`
	Items        int               `json:"items"`
}

type KnowledgeProjection struct {
	Evidence       []EvidenceValue  `json:"evidence"`
	Sources        []SourceCoverage `json:"sources"`
	OmittedSources int              `json:"omitted_sources"`
	Truncated      bool             `json:"truncated"`
}

func (s *Store) EvidenceFreshness(ref EvidenceReference) string {
	p, ok := s.snapshot.Knowledge[ref.Partition]
	if !ok {
		return "missing"
	}
	if p.Metadata.Stale {
		return "stale"
	}
	if p.Metadata.Version != ref.Version {
		return "superseded"
	}
	return "current"
}

func pointerPart(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

func decodeKnowledge(raw json.RawMessage) any {
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	_ = d.Decode(&v)
	return v
}

type projectionGroup struct {
	coverage SourceCoverage
	value    any
	limit    int
	queue    []EvidenceValue
}

// ProjectKnowledge allocates equal byte shares to partitions, then to their MCP
// sources. Selection proceeds in rounds across groups: neither serialization
// size nor an early sort position can let one source consume all evidence slots.
// Cardinality overflow is explicit; coverage never claims omitted data was seen.
func (s *Store) ProjectKnowledge(before Snapshot, preferred []EvidenceReference, budget, maxEntries, maxValue int) KnowledgeProjection {
	out := KnowledgeProjection{Evidence: []EvidenceValue{}, Sources: []SourceCoverage{}}
	partitions := make([]string, 0, len(s.snapshot.Knowledge))
	for name := range s.snapshot.Knowledge {
		partitions = append(partitions, name)
	}
	sort.Strings(partitions)
	byPartition := make([][]*projectionGroup, 0, len(partitions))
	for _, name := range partitions {
		p := s.snapshot.Knowledge[name]
		root := decodeKnowledge(p.Value)
		groups := []*projectionGroup{}
		add := func(path string, value any) {
			ref := EvidenceReference{Partition: name, Version: p.Metadata.Version, Path: path}
			kind, items := "scalar", 1
			switch v := value.(type) {
			case map[string]any:
				kind, items = "object", len(v)
			case []any:
				kind, items = "array", len(v)
			case string:
				kind = "string"
			}
			groups = append(groups, &projectionGroup{coverage: SourceCoverage{Reference: ref, Freshness: s.EvidenceFreshness(ref), Coverage: "partial", Type: kind, Items: items}, value: value})
		}
		// Reuse the existing source envelope, including custom partition names.
		// No domain-specific fields or tool meanings are inferred here.
		obj, _ := root.(map[string]any)
		sources, _ := obj["sources"].(map[string]any)
		if len(sources) > 0 {
			servers := sortedKeys(sources)
			for _, server := range servers {
				tools, ok := sources[server].(map[string]any)
				if !ok {
					add("/sources/"+pointerPart(server), sources[server])
					continue
				}
				for _, tool := range sortedKeys(tools) {
					add("/sources/"+pointerPart(server)+"/"+pointerPart(tool), tools[tool])
				}
			}
			for _, key := range sortedKeys(obj) {
				if key != "sources" {
					add("/"+pointerPart(key), obj[key])
				}
			}
		} else {
			add("", root)
		}
		byPartition = append(byPartition, groups)
	}
	if len(partitions) == 0 {
		return out
	}
	// Reserve a share for inventory independently of payload size. Interleave
	// partitions even when inventory cardinality itself exceeds the hard budget.
	groups := []*projectionGroup{}
	for round := 0; ; round++ {
		found := false
		for _, pg := range byPartition {
			if round >= len(pg) {
				continue
			}
			found = true
			g := pg[round]
			if len(g.coverage.Reference.Path) > 256 || len(encode(out.Sources))+len(encode(g.coverage))+256 > budget/3 {
				out.OmittedSources++
				out.Truncated = true
				continue
			}
			g.limit = max(0, (budget*2/3)/len(partitions)/max(1, len(pg)))
			groups = append(groups, g)
			out.Sources = append(out.Sources, g.coverage)
		}
		if !found {
			break
		}
	}
	for _, g := range groups {
		if g.coverage.Freshness != "current" {
			g.coverage.Omitted = 1
			continue
		}
		ref := g.coverage.Reference
		limit := min(maxValue, max(0, g.limit-256-len(encode(ref))))
		seen := []string{}
		previousRoot := decodeKnowledge(before.Knowledge[ref.Partition].Value)
		add := func(path string, value any) bool {
			if len(path) > 256 || len(g.queue) >= maxEntries {
				return false
			}
			for _, prior := range seen {
				if path == prior || strings.HasPrefix(path, prior+"/") {
					return true
				}
				if strings.HasPrefix(prior, path+"/") {
					return false
				}
			}
			raw := encode(value)
			if len(raw) > limit {
				return false
			}
			r := ref
			r.Path = path
			old, present := valueAt(previousRoot, path)
			_, partitionPresent := before.Knowledge[r.Partition]
			recent := !partitionPresent || !present || !reflect.DeepEqual(old, value)
			// MCP source sequence identifies an intentional unchanged refresh.
			if obj, ok := g.value.(map[string]any); ok {
				if seq, ok := obj["sequence"]; ok {
					oldSequence, _ := valueAt(previousRoot, ref.Path+"/sequence")
					recent = recent || !reflect.DeepEqual(oldSequence, seq)
				}
			}
			g.queue = append(g.queue, EvidenceValue{Reference: r, Value: raw, Freshness: "current", Recent: recent})
			seen = append(seen, path)
			return true
		}
		duplicatePaths := []string{}
		if obj, ok := g.value.(map[string]any); ok {
			result, _ := obj["result"].(map[string]any)
			content, _ := result["content"].([]any)
			if structured, present := result["structuredContent"]; present {
				for i, block := range content {
					item, _ := block.(map[string]any)
					txt, _ := item["text"].(string)
					if item["type"] == "text" && json.Valid([]byte(txt)) && reflect.DeepEqual(decodeKnowledge([]byte(txt)), structured) {
						path := ref.Path + "/result/content"
						if len(content) > 1 {
							path = fmt.Sprintf("%s/%d", path, i)
						}
						duplicatePaths = append(duplicatePaths, path)
					}
				}
			}
		}
		g.coverage.Deduplicated = len(duplicatePaths) > 0
		isDuplicate := func(path string) bool {
			for _, dup := range duplicatePaths {
				if path == dup || strings.HasPrefix(path, dup+"/") {
					return true
				}
			}
			return false
		}
		containsDuplicate := func(path string) bool {
			for _, dup := range duplicatePaths {
				if strings.HasPrefix(dup, path+"/") {
					return true
				}
			}
			return false
		}

		for _, r := range preferred {
			if isDuplicate(r.Path) || containsDuplicate(r.Path) {
				continue
			}
			if r.Partition == ref.Partition && (r.Path == ref.Path || strings.HasPrefix(r.Path, ref.Path+"/")) {
				r.Version = ref.Version
				if v, present := valueAt(g.value, strings.TrimPrefix(r.Path, ref.Path)); present {
					add(r.Path, v)
				}
			}
		}
		var walk func(any, string)
		walk = func(value any, path string) {
			if isDuplicate(path) {
				return
			}
			if len(path) > 256 || len(g.queue) >= maxEntries {
				g.coverage.Omitted++
				return
			}
			if !containsDuplicate(path) {
				if add(path, value) {
					return
				}
			}
			switch v := value.(type) {
			case map[string]any:
				if len(v) == 0 {
					g.coverage.Omitted++
				}
				keys := sortedKeys(v)
				// Prefer observation data over routing metadata within a source's
				// fair share, without interpreting any tool-specific payload fields.
				preferredKey := ""
				if path == ref.Path {
					preferredKey = "result"
				} else if path == ref.Path+"/result" {
					preferredKey = "structuredContent"
				}
				if _, ok := v[preferredKey]; ok {
					keys = append([]string{preferredKey}, keys...)
					for i := 1; i < len(keys); i++ {
						if keys[i] == preferredKey {
							keys = append(keys[:i], keys[i+1:]...)
							break
						}
					}
				}
				for _, key := range keys {
					walk(v[key], path+"/"+pointerPart(key))
				}
			case []any:
				if len(v) == 0 {
					g.coverage.Omitted++
				}
				for i, child := range v {
					walk(child, fmt.Sprintf("%s/%d", path, i))
				}
			default:
				g.coverage.Omitted++
			}
		}
		walk(g.value, ref.Path)
	}
	spent := make([]int, len(groups))
	for round := 0; ; round++ {
		found := false
		for i, g := range groups {
			if round >= len(g.queue) {
				continue
			}
			found = true
			e := g.queue[round]
			size := len(encode(e)) + 1
			if len(out.Evidence) >= maxEntries || spent[i]+size > g.limit {
				g.coverage.Omitted++
				continue
			}
			out.Evidence = append(out.Evidence, e)
			if len(encode(out))+len(encode(g.coverage))+256 > budget {
				out.Evidence = out.Evidence[:len(out.Evidence)-1]
				g.coverage.Omitted++
				continue
			}
			spent[i] += size
			g.coverage.Selected++
		}
		if !found {
			break
		}
	}
	for i, g := range groups {
		if g.coverage.Omitted == 0 {
			g.coverage.Coverage = "complete"
		} else {
			out.Truncated = true
		}
		out.Sources[i] = g.coverage
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func valueAt(v any, path string) (any, bool) {
	if path == "" {
		return v, true
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch node := v.(type) {
		case map[string]any:
			var ok bool
			v, ok = node[part]
			if !ok {
				return nil, false
			}
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(node) || strconv.Itoa(i) != part {
				return nil, false
			}
			v = node[i]
		default:
			return nil, false
		}
	}
	return v, true
}
