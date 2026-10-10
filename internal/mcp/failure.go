package mcp

import (
	"context"
	"encoding/json"
	"sort"
)

// InvocationFailure contains only host-defined categories and safe diagnostics.
// It never wraps remote errors, response payloads, or schema instance paths.
type InvocationFailure struct {
	Category           string         `json:"category"`
	Execution          string         `json:"execution"`
	Details            map[string]int `json:"details,omitempty"`
	ValidationKeywords []string       `json:"validation_keywords,omitempty"`
}

func (f *InvocationFailure) Error() string { return "mcp: " + f.Category }
func invocationFailure(ctx context.Context, category, execution string, details map[string]int, validation json.RawMessage) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &InvocationFailure{Category: category, Execution: execution, Details: details, ValidationKeywords: safeValidationKeywords(validation)}
}

// Only fixed JSON Schema keyword names are safe to echo. Diagnostics can contain
// arbitrary remote property names, schema text and actual values; discard those.
func safeValidationKeywords(raw json.RawMessage) []string {
	allowed := map[string]bool{"type": true, "required": true, "additionalProperties": true, "enum": true, "const": true, "oneOf": true, "anyOf": true, "allOf": true, "minItems": true, "maxItems": true, "minLength": true, "maxLength": true, "minimum": true, "maximum": true, "pattern": true}
	found := map[string]bool{}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if errs, ok := x["errors"].(map[string]any); ok {
				for k := range errs {
					if allowed[k] {
						found[k] = true
					}
				}
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(v)
	var out []string
	for k := range found {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
