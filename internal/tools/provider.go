// internal/tools/provider.go
package tools

import "context"

// Provider interface defines the contract for tool providers
type Provider interface {
	Name() string

	Discover(
		ctx context.Context,
	) ([]ToolDefinition, error)

	Execute(
		ctx context.Context,
		call ToolCall,
	) (ToolResult, error)
}
