package observation

import (
	"encoding/json"
	"time"
)

// Observation represents information entering the execution loop
type Observation struct {
	ID        string          `json:"id"`
	Source    string          `json:"source"`
	Tool      string          `json:"tool,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data,omitempty"`
	Error     *ErrorInfo      `json:"error,omitempty"`
}

// ErrorInfo represents error information in an observation
type ErrorInfo struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
