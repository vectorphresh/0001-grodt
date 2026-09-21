package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	sdk "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const ToolGetWalletBalance = "get_wallet_balance"

func registerTools(s *server.MCPServer, wallet Wallet) {
	tool := sdk.NewTool(
		ToolGetWalletBalance,
		sdk.WithDescription(
			"Return the current authoritative balance of the agent's wallet.",
		),
	)

	s.AddTool(tool, getWalletBalanceHandler(wallet))
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
