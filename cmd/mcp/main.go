package main

import (
	"context"
	"log"
	"os"

	"github.com/ethereum/go-ethereum/ethclient"

	grodt "github.com/vectorphresh/0001-grodt/internal/mcp"
)

func main() {
	wallet := newWallet()

	s := grodt.NewServer(wallet)

	if err := grodt.Serve(s); err != nil {
		log.Fatal(err)
	}
}

func newWallet() grodt.Wallet {
	walletType := os.Getenv("GRODT_WALLET")
	log.Printf("GRODT_WALLET=%q", walletType)

	switch walletType {
	case "", "memory":
		log.Printf("using MemoryWallet")
		// ...
	case "evm":
		log.Printf("using EVMWallet")
		return newEVMWallet()
		// ...
	}
	switch os.Getenv("GRODT_WALLET") {
	case "", "memory":
		// The memory wallet provides a deterministic implementation for
		// development, MCP debugging, and tests without requiring access
		// to an EVM network.
		return grodt.NewMemoryWallet(
			"ETH",
			"1.000000000000000000",
		)

	case "evm":
		return newEVMWallet()

	default:
		log.Fatalf(
			"unsupported GRODT_WALLET %q",
			os.Getenv("GRODT_WALLET"),
		)
		return nil
	}
}

func newEVMWallet() grodt.Wallet {
	rpcURL := os.Getenv("GRODT_RPC_URL")
	if rpcURL == "" {
		log.Fatal("GRODT_RPC_URL is required for EVM wallet")
	}

	address := os.Getenv("GRODT_WALLET_ADDRESS")
	if address == "" {
		log.Fatal("GRODT_WALLET_ADDRESS is required for EVM wallet")
	}

	client, err := ethclient.DialContext(context.Background(), rpcURL)
	if err != nil {
		log.Fatalf("connect to EVM RPC: %v", err)
	}

	wallet, err := grodt.NewEVMWallet(client, address)
	if err != nil {
		log.Fatalf("create EVM wallet: %v", err)
	}

	return wallet
}
