package loop

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	runstate "github.com/vectorphresh/0001-grodt/internal/state"
)

func TestReconciliationIncludesLargerEvidenceValue(t *testing.T) {
	// Crypto intraday CSV is a scalar inside structuredContent. It must remain
	// exact evidence rather than leaving only successful-execution metadata.
	payload := mustEncode(map[string]string{"result": strings.Repeat("timestamp,open,high,low,close,volume\n", 220)})
	initial := mustEncode(map[string]any{"sources": map[string]any{"alpha_vantage": map[string]any{"CRYPTO_INTRADAY": map[string]any{"result": map[string]any{"structuredContent": payload, "content": []any{map[string]any{"type": "text", "text": string(payload)}}, "isError": false}}}}})
	p, _ := toolsHarness(t, []runstate.Definition{{Name: "mcp_tools", Schema: json.RawMessage(`{"type":"object"}`), Initial: initial, Module: &eventModule{}}}, false)
	seedProcedure(t, p, "step_2", "Fetch cryptocurrency data")
	input := p.reconciliationInput(p.Store.Snapshot(), "", nil)
	for _, evidence := range input.AcceptedEvidence {
		if evidence.Reference.Path == "/sources/alpha_vantage/CRYPTO_INTRADAY/result/structuredContent" {
			if !bytes.Equal(evidence.Value, payload) {
				t.Fatal("evidence payload changed")
			}
			if len(evidence.Value) <= 4<<10 || len(evidence.Value) > maxEvidenceValue || len(mustEncode(input)) > maxReconciliationInput {
				t.Fatal("evidence or envelope size outside expected bounds")
			}
			return
		}
	}
	t.Fatal("crypto payload omitted from accepted evidence")
}
