package mcp

import "github.com/mark3labs/mcp-go/server"

const (
	ServerName    = "grodt"
	ServerVersion = "0.0.1"
)

// NewServer constructs GRODT's MCP interface.
//
// The MCP layer should remain an adapter around capabilities owned elsewhere.
// Tool handlers may translate protocol requests and responses, but economic
// policy and agent decision-making do not belong here.
func NewServer(wallet Wallet) *server.MCPServer {
	s := server.NewMCPServer(
		ServerName,
		ServerVersion,
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)

	registerTools(s, wallet)

	return s
}

// Serve starts the stdio MCP transport.
//
// stdout belongs exclusively to the MCP protocol. Diagnostic output must go to
// stderr; writing arbitrary logs to stdout can corrupt the JSON-RPC stream.
func Serve(s *server.MCPServer) error {
	return server.ServeStdio(s)
}
