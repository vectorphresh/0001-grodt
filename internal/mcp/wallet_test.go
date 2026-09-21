package mcp

import (
	"context"
	"testing"

	sdk "github.com/mark3labs/mcp-go/mcp"
)

func TestGetWalletBalance(t *testing.T) {
	wallet := NewMemoryWallet(
		"ETH",
		"1.000000000000000000",
	)

	handler := getWalletBalanceHandler(wallet)

	result, err := handler(
		context.Background(),
		sdk.CallToolRequest{},
	)
	if err != nil {
		t.Fatalf("getWalletBalanceHandler() error = %v", err)
	}

	if result.IsError {
		t.Fatalf("getWalletBalanceHandler() returned tool error: %+v", result)
	}

	if len(result.Content) != 1 {
		t.Fatalf(
			"getWalletBalanceHandler() content count = %d, want 1",
			len(result.Content),
		)
	}
}
