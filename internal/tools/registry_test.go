package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/tools"
)

func provider(name string) *tools.FakeProvider {
	return &tools.FakeProvider{
		ProviderName: name,
		Tools:        []tools.ToolDefinition{{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Results:      map[string]tools.ToolResult{"read": {Data: json.RawMessage(`{"ok":true}`)}},
	}
}

func TestNamespacingDiscoveryAndRouting(t *testing.T) {
	ctx := context.Background()
	registry := tools.NewRegistry()
	a, b := provider("alpha"), provider("beta")
	for _, p := range []*tools.FakeProvider{b, a} {
		if err := registry.Register(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	// Discovery is a startup snapshot, not repeated on every decision or call.
	a.DiscoverError = errors.New("must not rediscover")
	a.Tools[0].Name = "changed"
	listed, err := registry.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Name != "alpha.read" || listed[1].Name != "beta.read" || listed[0].Source != "alpha" {
		t.Fatalf("definitions: %+v", listed)
	}
	listed[0].InputSchema[0] = '!'
	def, exists := registry.Get(ctx, "alpha.read")
	if !exists || !json.Valid(def.InputSchema) {
		t.Fatal("definition mutation leaked")
	}
	for _, name := range []string{"alpha.read", "beta.read"} {
		result, err := registry.Execute(ctx, tools.ToolCall{Name: name, Arguments: json.RawMessage(`{}`)})
		if err != nil || result.Tool != name {
			t.Fatalf("result: %+v, %v", result, err)
		}
	}
	if len(a.Calls) != 1 || len(b.Calls) != 1 || a.Calls[0].Name != "read" || b.Calls[0].Name != "read" {
		t.Fatal("incorrect provider/remote-name routing")
	}
	if _, exists := registry.Get(ctx, "read"); exists {
		t.Fatal("unnamespaced tool resolved")
	}
	if _, err := registry.Execute(ctx, tools.ToolCall{Name: "unknown.read"}); err == nil {
		t.Fatal("unknown tool executed")
	}
}

func TestRegistrationFailuresAreAtomic(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(*tools.FakeProvider){
		"discovery":      func(p *tools.FakeProvider) { p.DiscoverError = errors.New("discovery failed") },
		"duplicate tool": func(p *tools.FakeProvider) { p.Tools = append(p.Tools, p.Tools[0]) },
		"bad input schema": func(p *tools.FakeProvider) {
			p.Tools = append(p.Tools, tools.ToolDefinition{Name: "bad", InputSchema: json.RawMessage(`{"type":"not-a-type"}`)})
		},
		"bad output schema": func(p *tools.FakeProvider) { p.Tools[0].OutputSchema = json.RawMessage(`{"required":true}`) },
		"external ref":      func(p *tools.FakeProvider) { p.Tools[0].InputSchema = json.RawMessage(`{"$ref":"file:///etc/passwd"}`) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			registry := tools.NewRegistry()
			p := provider("alpha")
			change(p)
			if err := registry.Register(ctx, p); err == nil {
				t.Fatal("registration accepted")
			}
			listed, err := registry.List(ctx)
			if err != nil || len(listed) != 0 {
				t.Fatalf("partial registration: %+v %v", listed, err)
			}
			if err := registry.Register(ctx, provider("alpha")); err != nil {
				t.Fatalf("failed registration reserved name: %v", err)
			}
		})
	}
}

func TestDuplicateProviderDoesNotReplaceOriginal(t *testing.T) {
	ctx := context.Background()
	registry := tools.NewRegistry()
	original := provider("alpha")
	if err := registry.Register(ctx, original); err != nil {
		t.Fatal(err)
	}
	replacement := provider("alpha")
	if err := registry.Register(ctx, replacement); err == nil {
		t.Fatal("duplicate provider accepted")
	}
	if _, err := registry.Execute(ctx, tools.ToolCall{Name: "alpha.read", Arguments: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if len(original.Calls) != 1 || len(replacement.Calls) != 0 {
		t.Fatal("original provider replaced")
	}
}

func TestProviderErrorPreserved(t *testing.T) {
	ctx := context.Background()
	registry := tools.NewRegistry()
	p := provider("alpha")
	sentinel := errors.New("provider failed")
	p.ExecuteError = sentinel
	if err := registry.Register(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(ctx, tools.ToolCall{Name: "alpha.read"}); !errors.Is(err, sentinel) {
		t.Fatalf("error: %v", err)
	}
}

func TestJSONSchemaValidation(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["order"],"properties":{"order":{"$ref":"#/$defs/order"}},"additionalProperties":false,"$defs":{"order":{"type":"object","required":["side","quantity"],"properties":{"side":{"enum":["buy","sell"]},"quantity":{"type":"integer","minimum":1}},"additionalProperties":false}}}`)
	for _, tc := range []struct {
		data  string
		valid bool
	}{
		{`{"order":{"side":"buy","quantity":2}}`, true},
		{`{"order":{"side":"invalid","quantity":2}}`, false},
		{`{"order":{"side":"buy","quantity":1.5}}`, false},
		{`{"order":{"side":"buy","quantity":0}}`, false},
		{`{"order":{"side":"buy"}}`, false},
		{`{"order":{"side":"buy","quantity":2,"extra":true}}`, false},
		{`{} {}`, false},
		{`null`, false},
	} {
		t.Run(tc.data, func(t *testing.T) {
			err := tools.ValidateInput(tools.ToolDefinition{InputSchema: schema}, json.RawMessage(tc.data))
			if (err == nil) != tc.valid {
				t.Fatalf("validation: %v", err)
			}
		})
	}
	if err := tools.ValidateSchema(nil, json.RawMessage(`not json`)); err == nil {
		t.Fatal("invalid output accepted without schema")
	}
	if err := tools.ValidateInput(tools.ToolDefinition{}, json.RawMessage(`[]`)); err == nil {
		t.Fatal("non-object arguments accepted without schema")
	}
}
