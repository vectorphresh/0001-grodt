package mcp

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
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
	Address string `json:"address"`
	Asset   string `json:"asset"`
	Amount  string `json:"amount"`
	Wei     string `json:"wei"`
}

// MemoryWallet is a deterministic wallet implementation used while bringing up
// the MCP boundary and in unit tests.
type MemoryWallet struct {
	balance Balance
}

type EVMWallet struct {
	client  *ethclient.Client
	address common.Address
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

func NewEVMWallet(client *ethclient.Client, address string) (*EVMWallet, error) {
	if !common.IsHexAddress(address) {
		return nil, fmt.Errorf("invalid EVM address %q", address)
	}

	return &EVMWallet{
		client:  client,
		address: common.HexToAddress(address),
	}, nil
}

func (w *EVMWallet) Balance(ctx context.Context) (Balance, error) {
	wei, err := w.client.BalanceAt(ctx, w.address, nil)
	if err != nil {
		return Balance{}, fmt.Errorf("query balance: %w", err)
	}

	return Balance{
		Address: w.address.Hex(),
		Asset:   "ETH",
		Amount:  formatEther(wei),
		Wei:     wei.String(),
	}, nil
}

func formatEther(wei *big.Int) string {
	value := new(big.Rat).SetInt(wei)
	value.Quo(value, big.NewRat(1_000_000_000_000_000_000, 1))

	return value.FloatString(18)
}
