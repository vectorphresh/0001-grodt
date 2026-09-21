package main

import (
	"log"

	grodt "github.com/vectorphresh/0001-grodt/internal/mcp"
)

func main() {
	// Start with a deterministic wallet while validating the complete
	// llama.cpp -> MCP -> GRODT tool path. Replacing this with an EVM-backed
	// wallet should not require changing the MCP tool contract.
	wallet := grodt.NewMemoryWallet("ETH", "1.000000000000000000")

	s := grodt.NewServer(wallet)

	if err := grodt.Serve(s); err != nil {
		log.Fatal(err)
	}
}
