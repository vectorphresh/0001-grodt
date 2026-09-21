package mcp

import "time"

type MemoryInput struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type Memory struct {
	Key        string    `json:"key"`
	Value      string    `json:"value"`
	ObservedAt time.Time `json:"observed_at"`
}
