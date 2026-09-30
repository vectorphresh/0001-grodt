// Package validation checks JSON structure using caller-supplied JSON Schemas.
// Callers own the original data, retry policy, and any state changes.
package validation

import (
	"context"
	"encoding/json"
)

// Validator performs synchronous structural validation. Invalid data (including
// malformed JSON) is normal feedback; errors indicate schema or runtime failure.
type Validator interface {
	Validate(ctx context.Context, schema, data json.RawMessage) (Result, error)
}

// Result contains no copy of the input. Details is the native library evaluation
// tree encoded as JSON, including locations, constraints, parameters, and causes.
// Details is omitted on success. Its diagnostic shape is library-defined.
type Result struct {
	Valid   bool            `json:"valid"`
	Details json.RawMessage `json:"details,omitempty"`
}

// SchemaError identifies an unusable schema. Details contains structured
// meta-schema validation feedback when available; Err retains the failure cause.
type SchemaError struct {
	Err     error
	Details json.RawMessage
}

func (e *SchemaError) Error() string { return "invalid schema: " + e.Err.Error() }
func (e *SchemaError) Unwrap() error { return e.Err }
