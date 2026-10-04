// Package mcp adapts MCP sessions to GRODT's generic tool catalog. It owns no
// domain state and grants no execution authority to state modules.
package mcp

import "time"

const (
	MaxServers        = 8
	MaxDiscoveryPages = 32
	MaxTools          = 128
	MaxCatalogBytes   = 1 << 20
	MaxBatchCalls     = 8
	// MaxInvocations bounds a run independently of outer cycles, structured
	// corrections and the state module HTTP host-execution budget.
	MaxInvocations   = 32
	MaxArgumentBytes = 64 << 10
	MaxResultBytes   = 1 << 20
	// Wire framing and notification overhead is bounded separately from content.
	MaxWireBytes          = MaxResultBytes + (64 << 10)
	InitializationTimeout = 30 * time.Second
	InvocationTimeout     = 30 * time.Second
)
