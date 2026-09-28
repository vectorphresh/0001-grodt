package contracts

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/vectorphresh/0001-grodt/internal/openai"
)

var contractName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// New checks mechanical preconditions and copies the definition. Schemas must
// be JSON objects or booleans; no JSON Schema semantics are validated. Fragments
// are embedded as supplied, without resolving or rewriting $ref or $id.
func New(client openai.Client, definition Definition) (*Reducer, error) {
	if client == nil {
		return nil, errors.New("contracts: client is required")
	}
	if len(definition.Contracts) == 0 {
		return nil, errors.New("contracts: definition must not be empty")
	}
	r := &Reducer{client: client, byName: make(map[string]Contract)}
	for _, c := range definition.Contracts {
		if !contractName.MatchString(c.Name) {
			return nil, errors.New("contracts: invalid contract name")
		}
		if _, exists := r.byName[c.Name]; exists {
			return nil, errors.New("contracts: duplicate contract name")
		}
		if strings.TrimSpace(c.Description) == "" {
			return nil, errors.New("contracts: description is required")
		}
		schema := bytes.TrimSpace(c.Schema)
		if !json.Valid(schema) || !(schema[0] == '{' || bytes.Equal(schema, []byte("true")) || bytes.Equal(schema, []byte("false"))) {
			return nil, errors.New("contracts: schema must be a JSON object or boolean")
		}
		c.Schema = bytes.Clone(c.Schema)
		r.contracts = append(r.contracts, c)
		r.byName[c.Name] = c
	}
	return r, nil
}

func (r *Reducer) subset(names []string) (map[string]bool, error) {
	selected := make(map[string]bool, len(names))
	for _, name := range names {
		if _, ok := r.byName[name]; !ok {
			return nil, errors.New("contracts: unknown selected contract")
		}
		if selected[name] {
			return nil, errors.New("contracts: duplicate selected contract")
		}
		selected[name] = true
	}
	return selected, nil
}

// PartialSpecification constrains proposal shape only. Selected writable
// contracts are optional top-level properties. No meaning is assigned to their
// presence or omission. Empty names mean an empty subset, never all contracts.
func (r *Reducer) PartialSpecification(names []string) (openai.JSONSpecification, error) {
	selected, err := r.subset(names)
	if err != nil {
		return openai.JSONSpecification{}, err
	}
	properties := make(map[string]json.RawMessage)
	for _, c := range r.contracts {
		if selected[c.Name] && c.ModelWritable {
			properties[c.Name] = c.Schema
		}
	}
	schema, err := json.Marshal(struct {
		Type                 string                     `json:"type"`
		Properties           map[string]json.RawMessage `json:"properties"`
		AdditionalProperties bool                       `json:"additionalProperties"`
	}{"object", properties, false})
	if err != nil {
		return openai.JSONSpecification{}, errors.New("contracts: cannot compose specification")
	}
	return openai.JSONSpecification{Name: "contract_proposal", Description: "Proposed JSON for selected writable contracts; no application semantics are implied.", Schema: schema, Strict: true}, nil
}
