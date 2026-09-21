package mcp

import (
	"context"
	"fmt"
)

// Wallet provides the subset of wallet functionality exposed to the agent.
//
// Keeping the wallet behind this interface separates the MCP protocol from the
// mechanism used to obtain authoritative wallet state. The initial
// implementation can be entirely in memory; a later implementation can query
// an EVM node without changing the tool exposed to the model.
type Wallet interface {
	Balance(ctx context.Context) (Balance, error)
}

// Balance describes an authoritative wallet balance.
//
// Amount is represented as a string rather than a floating-point value because
// blockchain asset quantities must not lose precision during conversion.
type Balance struct {
	Asset  string `json:"asset"`
	Amount string `json:"amount"`
}

// MemoryWallet is a deterministic wallet implementation used while bringing up
// the MCP boundary and in unit tests.
type MemoryWallet struct {
	balance Balance
}

func NewMemoryWallet(asset, amount string) *MemoryWallet {
	return &MemoryWallet{
		balance: Balance{
			Asset:  asset,
			Amount: amount,
		},
	}
}

func (w *MemoryWallet) Balance(context.Context) (Balance, error) {
	if w == nil {
		return Balance{}, fmt.Errorf("wallet is nil")
	}

	return w.balance, nil
}
