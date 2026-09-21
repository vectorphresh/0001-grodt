package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	sdk "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	ToolGetWalletBalance    = "get_wallet_balance"
	ToolGetWalletChainState = "get_wallet_chain_state"
)

func registerTools(s *server.MCPServer, wallet Wallet) {
	balanceTool := sdk.NewTool(
		ToolGetWalletBalance,
		sdk.WithDescription(
			"Return the current authoritative balance of the agent's wallet.",
		),
	)

	chainStateTool := sdk.NewTool(
		ToolGetWalletChainState,
		sdk.WithDescription(
			"Return the authoritative chain ID and current block number for the wallet's network.",
		),
	)

	s.AddTool(
		balanceTool,
		getWalletBalanceHandler(wallet),
	)

	s.AddTool(
		chainStateTool,
		getWalletChainStateHandler(wallet),
	)
}

func getWalletBalanceHandler(wallet Wallet) server.ToolHandlerFunc {
	return func(
		ctx context.Context,
		request sdk.CallToolRequest,
	) (*sdk.CallToolResult, error) {
		balance, err := wallet.Balance(ctx)
		if err != nil {
			return sdk.NewToolResultError(
				fmt.Sprintf("get wallet balance: %v", err),
			), nil
		}

		data, err := json.Marshal(balance)
		if err != nil {
			return nil, fmt.Errorf("marshal wallet balance: %w", err)
		}

		return sdk.NewToolResultText(string(data)), nil
	}
}

func getWalletChainStateHandler(wallet Wallet) server.ToolHandlerFunc {
	return func(
		ctx context.Context,
		request sdk.CallToolRequest,
	) (*sdk.CallToolResult, error) {
		chainState, err := wallet.ChainState(ctx)
		if err != nil {
			return sdk.NewToolResultError(
				fmt.Sprintf("get wallet chain state: %v", err),
			), nil
		}

		data, err := json.Marshal(chainState)
		if err != nil {
			return nil, fmt.Errorf("marshal wallet chain state: %w", err)
		}

		return sdk.NewToolResultText(string(data)), nil
	}
}
