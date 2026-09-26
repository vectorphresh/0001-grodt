package state

import "context"

// Store interface defines the contract for state persistence
type Store interface {
	Load(ctx context.Context) (*AgentState, error)
	Save(ctx context.Context, state *AgentState) error
}
