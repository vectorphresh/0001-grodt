package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/vectorphresh/0001-grodt/internal/mcp"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

const (
	// strategyContextTool is the host-owned tool the actor uses to persist its
	// strategy context into the gr33d market_state partition.
	strategyContextTool = "grodt_manage_strategy_context"
	// DefaultStrategyContextBytes bounds the strategy_context string in UTF-8
	// bytes. It matches the reducer-side limit in the gr33d market_state module
	// so oversized payloads are rejected host-side with a deterministic error
	// instead of marking the partition stale.
	DefaultStrategyContextBytes = 8192
)

type strategyContextArguments struct {
	Operation       string `json:"operation"`
	StrategyContext string `json:"strategy_context"`
}

func strategyContextDefinition() toolcall.Definition {
	return toolcall.Definition{Name: strategyContextTool, Description: "Persist your strategy context. The strategy context is the durable record of your current strategy: what you are doing, why, and what you are watching for. It survives restarts and is the first strategy information you see again after a fresh session. Set it whenever your strategy meaningfully changes; clear it only when the strategy is genuinely retired. Submit this tool by itself.", InputSchema: json.RawMessage(`{"type":"object","required":["operation"],"properties":{"operation":{"type":"string","enum":["set","clear"],"description":"set persists strategy_context; clear empties the stored strategy context."},"strategy_context":{"type":"string","description":"Your strategy context text. Required for set; ignored for clear."}},"additionalProperties":false}`)}
}

type strategyContextPayload struct {
	Operation string          `json:"operation"`
	Status    string          `json:"status"`
	Result    json.RawMessage `json:"result"`
}

func decodeStrategyContextArguments(raw json.RawMessage) (strategyContextArguments, error) {
	var args strategyContextArguments
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return strategyContextArguments{}, errors.New("strategy context arguments do not satisfy the advertised schema")
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return strategyContextArguments{}, errors.New("strategy context arguments must contain one JSON object")
	}
	return args, nil
}

// manageStrategyContext handles the host-owned strategy context tool. The
// strategy context is admitted as an llm-kind event; the gr33d market_state
// reducer replaces the stored strategy_context string atomically and keeps
// market_state untouched. Returns handled=false when no call targets the tool.
func (p *ToolProvider) manageStrategyContext(ctx context.Context, calls []toolcall.Call) (bool, string, error) {
	found := false
	for _, call := range calls {
		if call.Name == strategyContextTool {
			found = true
			break
		}
	}
	if !found {
		return false, "", nil
	}
	if len(calls) != 1 || calls[0].ID == "" {
		return true, "", errors.New("call grodt_manage_strategy_context alone with a nonempty ID")
	}
	call := calls[0]
	if len(call.Arguments) > mcp.MaxArgumentBytes {
		return true, "", fmt.Errorf("strategy context arguments contain %d bytes; limit is %d", len(call.Arguments), mcp.MaxArgumentBytes)
	}
	args, err := decodeStrategyContextArguments(call.Arguments)
	if err != nil {
		return true, "", err
	}
	switch args.Operation {
	case "set":
		if args.StrategyContext == "" {
			return true, "", errors.New("strategy_context is required for set")
		}
	case "clear":
		args.StrategyContext = ""
	default:
		return true, "", fmt.Errorf("operation must be set or clear, got %q", args.Operation)
	}
	limit := p.strategyContextLimit()
	if len([]byte(args.StrategyContext)) > limit {
		return true, "", fmt.Errorf("strategy_context contains %d UTF-8 bytes; limit is %d. Shorten the strategy context", len([]byte(args.StrategyContext)), limit)
	}
	result, err := json.Marshal(struct {
		StrategyContext string `json:"strategy_context"`
	}{StrategyContext: args.StrategyContext})
	if err != nil {
		return true, "", err
	}
	payload, err := json.Marshal(strategyContextPayload{Operation: args.Operation, Status: "accepted", Result: result})
	if err != nil {
		return true, "", err
	}
	p.strategySequence++
	source := runstate.Source{
		Kind: "llm",
		ID:   fmt.Sprintf("%s/strategy/%d", p.Store.Snapshot().Intrinsic.RunID, p.strategySequence),
	}
	if _, err := p.Store.Admit(ctx, source, payload); err != nil {
		return true, "", err
	}
	if args.Operation == "clear" {
		return true, "Strategy context cleared.", nil
	}
	return true, "Strategy context stored.", nil
}

func (p *ToolProvider) strategyContextLimit() int {
	if p.StrategyContextBytes > 0 {
		return p.StrategyContextBytes
	}
	return DefaultStrategyContextBytes
}
