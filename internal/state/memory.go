package state

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	MaxWorkingMemoryEntries = 64
	// MaxMemoryEntryBytes includes the key, value, timestamp, and JSON escaping.
	// Measured with encoding/json.Marshal, not characters or tokenizer estimates.
	MaxMemoryEntryBytes = 4096
)

func validateMemoryEntry(entry MemoryEntry) error {
	if strings.TrimSpace(entry.Key) == "" || strings.TrimSpace(entry.Value) == "" {
		return fmt.Errorf("memory key and value are required")
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("memory entry must be JSON encodable")
	}
	if len(encoded) > MaxMemoryEntryBytes {
		return fmt.Errorf("memory entry exceeds %d JSON bytes", MaxMemoryEntryBytes)
	}
	return nil
}

// ValidateWorkingMemory enforces the same policy on patches and loaded/saved
// snapshots. It never evicts entries or rewrites their contents.
func ValidateWorkingMemory(entries []MemoryEntry) error {
	if len(entries) > MaxWorkingMemoryEntries {
		return fmt.Errorf("working memory exceeds %d entries", MaxWorkingMemoryEntries)
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if err := validateMemoryEntry(entry); err != nil {
			return err
		}
		if seen[entry.Key] {
			return fmt.Errorf("duplicate memory key")
		}
		seen[entry.Key] = true
	}
	return nil
}
