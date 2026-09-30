package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

type validator struct{}

// New returns a stateless validator safe for concurrent calls. Each call compiles
// independently; no caller schemas or data are retained between calls.
func New() Validator { return validator{} }

func (validator) Validate(ctx context.Context, schema, data json.RawMessage) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("validation context is required")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	compiler := offlineCompiler()
	checked, err := compiler.ValidateSchema(schema)
	if err != nil {
		return Result{}, &SchemaError{Err: err}
	}
	if !checked.IsValid() {
		details, err := json.Marshal(checked)
		if err != nil {
			return Result{}, fmt.Errorf("encode schema diagnostics: %w", err)
		}
		return Result{}, &SchemaError{Err: errors.New("meta-schema validation failed"), Details: details}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	compiled, err := compiler.Compile(schema)
	if err != nil {
		return Result{}, &SchemaError{Err: err}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	evaluation := compiled.ValidateJSON(data)
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if evaluation.IsValid() {
		return Result{Valid: true}, nil
	}
	details, err := json.Marshal(evaluation)
	if err != nil {
		return Result{}, fmt.Errorf("encode validation diagnostics: %w", err)
	}
	return Result{Details: details}, nil
}
