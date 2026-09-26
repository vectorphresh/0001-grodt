package state

import (
	"slices"
	"time"
)

// MemoryEntry is a compact conclusion. Key is its stable identifier.
type MemoryEntry struct {
	Key        string    `json:"key"`
	Value      string    `json:"value"`
	ObservedAt time.Time `json:"observed_at"`
}

// Intent represents an action the agent intends to take
type Intent struct {
	ID         string    `json:"id"`
	Action     string    `json:"action"`
	Arguments  string    `json:"arguments"`
	Executed   bool      `json:"executed"`
	ExecutedAt time.Time `json:"executed_at,omitempty"`
}

// AgentState represents the persistent execution state of the agent
type AgentState struct {
	Version        uint64        `json:"version"`
	UpdatedAt      time.Time     `json:"updated_at"`
	World          WorldState    `json:"world"`
	WorkingMemory  []MemoryEntry `json:"working_memory,omitempty"`
	PendingIntents []Intent      `json:"pending_intents,omitempty"`
}

// WorldState represents the trusted external state that the agent observes
type WorldState struct {
	ObservedAt   time.Time  `json:"observed_at"`
	Balances     []Balance  `json:"balances,omitempty"`
	Positions    []Position `json:"positions,omitempty"`
	ActiveOrders []Order    `json:"active_orders,omitempty"`
}

// Balance represents a wallet balance
type Balance struct {
	Asset  string `json:"asset"`
	Amount string `json:"amount"`
}

// Position represents a held position
type Position struct {
	Symbol   string `json:"symbol"`
	Quantity string `json:"quantity"`
	AvgPrice string `json:"avg_price"`
}

// Order represents an active order
type Order struct {
	ID       string `json:"id"`
	Symbol   string `json:"symbol"`
	Quantity string `json:"quantity"`
	Price    string `json:"price"`
	Status   string `json:"status"`
}

// Clone returns an independent snapshot, including all mutable collections.
func (s AgentState) Clone() AgentState {
	s.World.Balances = slices.Clone(s.World.Balances)
	s.World.Positions = slices.Clone(s.World.Positions)
	s.World.ActiveOrders = slices.Clone(s.World.ActiveOrders)
	s.WorkingMemory = slices.Clone(s.WorkingMemory)
	s.PendingIntents = slices.Clone(s.PendingIntents)
	return s
}
