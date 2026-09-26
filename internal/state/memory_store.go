package state

import (
	"context"
	"fmt"
	"sync"
)

// MemoryStore owns independent snapshots. Version and timestamps are set by Runtime.
type MemoryStore struct {
	mu    sync.RWMutex
	state AgentState
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

func (s *MemoryStore) Load(ctx context.Context) (*AgentState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := s.state.Clone()
	return &snapshot, nil
}

func (s *MemoryStore) Save(ctx context.Context, value *AgentState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if value == nil {
		return fmt.Errorf("cannot save nil state")
	}
	if err := ValidateWorkingMemory(value.WorkingMemory); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = value.Clone()
	return nil
}
