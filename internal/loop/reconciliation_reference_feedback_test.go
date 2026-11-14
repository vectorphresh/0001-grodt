package loop

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	runstate "github.com/vectorphresh/0001-grodt/internal/state"
)

func TestReconciliationReferenceFeedbackRepairsSlashInKey(t *testing.T) {
	p, _ := toolsHarness(t, []runstate.Definition{{Name: "mcp_tools", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{"sources":{"alpaca":{"get_crypto_latest_bar":{"result":{"structuredContent":{"data":{"bars":{"BTC/USD":{"c":82727.008}}}}}}}}}`), Module: &eventModule{}}}, false)
	seedProcedure(t, p, "step_4", "Execute trades")
	active, _ := p.Store.Active()
	parent := runstate.EvidenceReference{Partition: "mcp_tools", Version: 0, Path: "/sources/alpaca/get_crypto_latest_bar/result/structuredContent"}
	value, err := p.Store.ResolveEvidence(parent)
	if err != nil {
		t.Fatal(err)
	}
	input := reconciliationInput{TaskID: active.ID, Plan: active.Plan, AcceptedEvidence: []reconciliationEvidence{{Reference: parent, Value: value, Freshness: "current"}}}
	step := active.Plan.Steps[0]
	step.Status, step.Outcome = "partial", "Latest crypto bar acquired"
	step.InformationGaps = []string{"Trades have not been executed"}
	bad := parent
	bad.Path += "/data/bars/BTC/USD/c"
	step.Evidence = []runstate.EvidenceReference{bad}
	before := p.Store.Snapshot().Tasks
	proposal := reconciliationResult(t, "progress", []progressUpdate{{Step: step}}, "")
	if _, err := p.reconciliationPlan(input, proposal.JSON); err == nil {
		t.Fatal("unescaped object key accepted")
	} else {
		feedback := reconciliationHostCorrection(err, proposal.JSON)
		for _, want := range []string{string(mustEncode(parent)), "BTC~1USD", "~ becomes ~0", "valid reference does not establish completion"} {
			if !strings.Contains(feedback, want) {
				t.Fatalf("repair feedback missing %q: %s", want, feedback)
			}
		}
	}
	if !reflect.DeepEqual(before, p.Store.Snapshot().Tasks) {
		t.Fatal("feedback rewrote accepted state")
	}
	// Both advertised repairs are structurally valid; neither commits a claim.
	for _, ref := range []runstate.EvidenceReference{parent, {Partition: parent.Partition, Version: parent.Version, Path: parent.Path + "/data/bars/BTC~1USD/c"}} {
		step.Evidence = []runstate.EvidenceReference{ref}
		plan, err := p.reconciliationPlan(input, reconciliationResult(t, "progress", []progressUpdate{{Step: step}}, "").JSON)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Steps[0].Status != "partial" || !reflect.DeepEqual(before, p.Store.Snapshot().Tasks) {
			t.Fatal("reference repair changed completion or committed without evaluation")
		}
	}
}

func TestReconciliationReferenceFeedbackOnlySuggestsSuppliedAncestor(t *testing.T) {
	ref := runstate.EvidenceReference{Partition: "bars", Version: 2, Path: "/data/bars/BTC/USD/c"}
	input := reconciliationInput{AcceptedEvidence: []reconciliationEvidence{
		{Reference: runstate.EvidenceReference{Partition: "bars", Version: 1, Path: "/data/bars"}, Freshness: "superseded", Value: json.RawMessage(`{}`)},
		{Reference: runstate.EvidenceReference{Partition: "bars", Version: 2, Path: "/data"}, Freshness: "current", Value: json.RawMessage(`{}`)},
		{Reference: runstate.EvidenceReference{Partition: "bars", Version: 2, Path: "/data/bars"}, Freshness: "current", Value: json.RawMessage(`{}`)},
	}}
	err := reconciliationReferenceError("step", ref, input)
	want := string(mustEncode(input.AcceptedEvidence[2].Reference))
	if !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), `"version":1`) {
		t.Fatal("suggested stale reference or missed closest ancestor", err)
	}
}
