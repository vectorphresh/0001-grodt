package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Schemas must be self-contained: local $defs/$ref are supported, but discovery
// must not trigger arbitrary network or filesystem reads to resolve references.
type localOnlyLoader struct{}

func (localOnlyLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema reference is not supported: %s", url)
}

func decodeJSON(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("expected exactly one JSON value")
	}
	return value, nil
}

func compileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	doc, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(localOnlyLoader{})
	const location = "https://grodt.invalid/tool-schema.json"
	if err := compiler.AddResource(location, doc); err != nil {
		return nil, err
	}
	return compiler.Compile(location)
}

// ValidateSchema validates JSON even when no output schema was supplied.
func ValidateSchema(schema, data json.RawMessage) error {
	value, err := decodeJSON(data)
	if err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if len(schema) == 0 {
		return nil
	}
	compiled, err := compileSchema(schema)
	if err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}
	return compiled.Validate(value)
}

func ValidateInput(def ToolDefinition, arguments json.RawMessage) error {
	value, err := decodeJSON(arguments)
	if err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return fmt.Errorf("tool arguments must be a JSON object")
	}
	return ValidateSchema(def.InputSchema, arguments)
}
