package contracts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/structured"
)

const selectionInstructions = "Select zero, one, or multiple declared contracts that may be affected by the new information and semantic context. Favor recall when there is plausible durable relevance. Selection does not imply that a contract must change. Include read-only contracts when relevant, but do not invent names. Return an empty relevant array for information with no durable relevance."

func (r *Reducer) Select(ctx context.Context, information, semanticContext json.RawMessage) (Selection, error) {
	if err := checkContext(ctx); err != nil {
		return Selection{}, err
	}
	if !json.Valid(information) {
		return Selection{}, errors.New("contracts: information must contain valid JSON")
	}
	semanticContext, err := optionalContext(semanticContext)
	if err != nil {
		return Selection{}, err
	}
	type descriptor struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		ModelWritable bool   `json:"model_writable"`
	}
	descriptors := make([]descriptor, 0, len(r.contracts))
	names := make([]string, 0, len(r.contracts))
	for _, c := range r.contracts {
		descriptors = append(descriptors, descriptor{c.Name, c.Description, c.ModelWritable})
		names = append(names, c.Name)
	}
	data, err := json.Marshal(struct {
		Contracts   []descriptor    `json:"contracts"`
		Information json.RawMessage `json:"information"`
		Context     json.RawMessage `json:"context"`
	}{descriptors, information, semanticContext})
	if err != nil {
		return Selection{}, errors.New("contracts: cannot encode selection input")
	}
	schema, err := json.Marshal(map[string]any{
		"type": "object", "properties": map[string]any{"relevant": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": names}, "uniqueItems": true}},
		"required": []string{"relevant"}, "additionalProperties": false,
	})
	if err != nil {
		return Selection{}, errors.New("contracts: cannot encode selection specification")
	}
	spec := openai.JSONSpecification{Name: "contract_selection", Schema: schema, Strict: true}
	result, err := structured.Generate(ctx, spec.Schema, func(ctx context.Context, feedback json.RawMessage) (openai.JSONResult, error) {
		candidate, err := r.client.PromptWithSpecification(ctx, structured.WithFeedback(selectionInstructions, feedback), "Which declared contracts may be affected by this information?", data, spec)
		if err != nil {
			return candidate, operationError(ctx, err)
		}
		return candidate, nil
	})
	selection := Selection{Requests: result.Requests, Usage: result.Usage, UsageRequests: result.UsageRequests}
	if err != nil {
		return selection, err
	}
	if err := checkContext(ctx); err != nil {
		return selection, err
	}
	selectedNames, err := decodeSelection(result.JSON)
	if err != nil {
		return selection, err
	}
	selected, err := r.subset(selectedNames)
	if err != nil {
		return selection, err
	}
	selection.Names = make([]string, 0, len(selected))
	for _, c := range r.contracts {
		if selected[c.Name] {
			selection.Names = append(selection.Names, c.Name)
		}
	}
	return selection, nil
}

type selectionResponse struct {
	Relevant selectionNames `json:"relevant"`
}

// Pointer elements distinguish null from strings. The custom decoder preserves
// rejection of duplicate relevant fields, which encoding/json otherwise accepts.
type selectionNames []*string

func (names *selectionNames) UnmarshalJSON(data []byte) error {
	if *names != nil {
		return errors.New("contracts: duplicate selection field")
	}
	var values []*string
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	if values == nil {
		return errors.New("contracts: relevant must be an array")
	}
	*names = values
	return nil
}

func decodeSelection(data []byte) ([]string, error) {
	invalid := errors.New("contracts: invalid selection response")
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var response selectionResponse
	if err := d.Decode(&response); err != nil || response.Relevant == nil {
		return nil, invalid
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, invalid
	}
	// Struct decoding matches keys case-insensitively. Preserve the exact envelope
	// key required by the specification without manually parsing object tokens.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields["relevant"] == nil {
		return nil, invalid
	}
	names := make([]string, len(response.Relevant))
	for i, v := range response.Relevant {
		if v == nil {
			return nil, invalid
		}
		names[i] = *v
	}
	return names, nil
}
