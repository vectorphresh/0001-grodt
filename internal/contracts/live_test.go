//go:build live

package contracts_test

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/contracts"
	"github.com/vectorphresh/0001-grodt/internal/openai"
)

// Run explicitly with OPENAI_BASE_URL and OPENAI_API_KEY set:
// go test -tags live ./internal/contracts -run '^TestLive' -count=1 -v
// These observations exercise generation, not validation or state application.
func TestLiveContracts(t *testing.T) {
	base, key := os.Getenv("OPENAI_BASE_URL"), os.Getenv("OPENAI_API_KEY")
	if base == "" || strings.TrimSpace(key) == "" {
		t.Fatal("live tests require OPENAI_BASE_URL and OPENAI_API_KEY")
	}
	client, err := openai.NewClient(openai.Config{BaseURL: base, APIKey: key, Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	reducer, err := contracts.New(client, contracts.Definition{Contracts: []contracts.Contract{
		{Name: "objective", Description: "The stable declared run objective, not observations of progress toward it.", Schema: json.RawMessage(`{"type":"string"}`)},
		{Name: "account", Description: "Authoritative account equity, cash, and buying power figures.", ModelWritable: true, Schema: json.RawMessage(`{"type":"object","properties":{"equity":{"type":"number"},"cash":{"type":"number"},"buying_power":{"type":"number"}},"additionalProperties":false}`)},
		{Name: "positions", Description: "Current open instruments and quantities based on authoritative holding observations.", ModelWritable: true, Schema: json.RawMessage(`{"type":"array","items":{"type":"object","properties":{"symbol":{"type":"string"},"quantity":{"type":"number"}},"required":["symbol","quantity"],"additionalProperties":false}}`)},
		{Name: "memory", Description: "Durable user preferences and guidance for future reasoning. Excludes balances, holdings, and transient diagnostics.", ModelWritable: true, Schema: json.RawMessage(`{"type":"object","properties":{"note":{"type":"string"}},"required":["note"],"additionalProperties":false}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, information, semanticContext string
		required, forbidden                []string
		empty                              bool
	}{
		{"AccountObservation", `{"equity":11000,"cash":7000,"buying_power":14000}`, `{"objective":"Grow account equity","source":"Authoritative account balance observation; no holdings or preference information."}`, []string{"account"}, []string{"positions", "memory"}, false},
		{"PositionClosure", `{"closed_position":"ABC","current_open_positions":[],"account":{"equity":11000,"cash":11000,"buying_power":22000}}`, `{"intent":"Realize profit","source":"Confirmed execution and authoritative account/holdings observation."}`, []string{"account", "positions"}, []string{"memory"}, false},
		{"MemoryRelevant", `"For all future reports, use UTC timestamps."`, `{"source":"Durable user reporting preference; no financial observation."}`, []string{"memory"}, []string{"account", "positions"}, false},
		{"NoDurableChange", `{"heartbeat":"ok","request_latency_ms":12}`, `{"source":"Transient diagnostic only; no new account, holdings, objective, or durable preference information."}`, nil, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := contracts.Input{State: json.RawMessage(`{"objective":"Grow account equity","account":{"equity":10000,"cash":5000,"buying_power":10000},"positions":[{"symbol":"ABC","quantity":10}],"memory":{"note":""}}`), Information: json.RawMessage(tc.information), Context: json.RawMessage(tc.semanticContext)}
			before := string(input.State)
			selection, err := reducer.Select(context.Background(), input.Information, input.Context)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("Selection: %v; requests=%d; usage=%+v", selection.Names, selection.Requests, selection.Usage)
			requireSelected(t, selection.Names, tc.required...)
			forbidSelected(t, selection.Names, tc.forbidden...)
			if selection.Requests != 1 {
				t.Fatalf("selection requests = %d; want 1", selection.Requests)
			}
			if tc.empty && len(selection.Names) != 0 {
				t.Fatalf("expected no durable relevance; selected %v", selection.Names)
			}
			proposal, err := reducer.Reduce(context.Background(), input, selection.Names)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("Proposed JSON: %s; requests=%d; usage=%+v", proposal.JSON, proposal.Requests, proposal.Usage)
			if string(input.State) != before {
				t.Fatal("caller state changed")
			}
			if len(selection.Names) == 0 {
				if string(proposal.JSON) != "{}" || proposal.Requests != 0 || proposal.Usage != nil {
					t.Fatal("empty selection made a mutation request")
				}
				return
			}
			if proposal.Requests != 1 {
				t.Fatal("expected one mutation request")
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(proposal.JSON, &fields); err != nil {
				t.Fatal(err)
			}
			// Scenario-specific evidence that useful observations reached generation.
			// No proposal is applied, and no meaning is assigned to omitted fields.
			switch tc.name {
			case "AccountObservation", "PositionClosure":
				var account struct {
					Equity float64 `json:"equity"`
				}
				if json.Unmarshal(fields["account"], &account) != nil || account.Equity != 11000 {
					t.Fatal("proposal did not convey observed equity")
				}
				if tc.name == "PositionClosure" {
					var positions []json.RawMessage
					if json.Unmarshal(fields["positions"], &positions) != nil || positions == nil || len(positions) != 0 {
						t.Fatal("proposal did not convey observed empty holdings")
					}
				}
			case "MemoryRelevant":
				var memory struct {
					Note string `json:"note"`
				}
				if json.Unmarshal(fields["memory"], &memory) != nil || !strings.Contains(memory.Note, "UTC") {
					t.Fatal("proposal did not convey reporting preference")
				}
			}
		})
	}
}

// Unspecified contracts are acceptable either way; compare exact declared names.
func requireSelected(t *testing.T, selected []string, required ...string) {
	t.Helper()
	for _, name := range required {
		if !slices.Contains(selected, name) {
			t.Fatalf("missing required contract %q; selected %v", name, selected)
		}
	}
}

func forbidSelected(t *testing.T, selected []string, forbidden ...string) {
	t.Helper()
	for _, name := range forbidden {
		if slices.Contains(selected, name) {
			t.Fatalf("selected forbidden contract %q; selected %v", name, selected)
		}
	}
}
