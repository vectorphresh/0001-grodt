package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	jsonpatch "github.com/evanphx/json-patch/v5"
)

type moduleResult struct {
	Requests []HostRequest   `json:"requests,omitempty"`
	Status   string          `json:"status"`
	Replace  json.RawMessage `json:"replace,omitempty"`
	Patch    json.RawMessage `json:"patch,omitempty"`
}

func decodeResult(data []byte) (moduleResult, error) {
	var r moduleResult
	if len(data) > MaxValueBytes {
		return r, errors.New("output limit")
	}
	// Token decoding rejects duplicate fields as well as unknown/conflicting ones.
	d := json.NewDecoder(bytes.NewReader(data))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return r, errors.New("invalid result")
	}
	seen := map[string]bool{}
	for d.More() {
		k, err := d.Token()
		if err != nil {
			return r, err
		}
		key, ok := k.(string)
		if !ok || seen[key] {
			return r, errors.New("duplicate result field")
		}
		seen[key] = true
		switch key {
		case "status":
			err = d.Decode(&r.Status)
		case "replace":
			err = d.Decode(&r.Replace)
		case "requests":
			d.DisallowUnknownFields()
			err = d.Decode(&r.Requests)
		case "patch":
			err = d.Decode(&r.Patch)
		default:
			return r, errors.New("unknown result field")
		}
		if err != nil {
			return r, err
		}
	}
	if _, err = d.Token(); err != nil {
		return r, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return r, errors.New("trailing result")
	}
	switch r.Status {
	case "ignored", "processed", "error":
		if seen["replace"] || seen["patch"] {
			return r, errors.New("unexpected mutation")
		}
	case "mutation":
		if seen["replace"] == seen["patch"] {
			return r, errors.New("exactly one mutation required")
		}
	default:
		return r, errors.New("invalid status")
	}
	if len(r.Requests) > MaxRequestsPerResult || (seen["requests"] && (r.Requests == nil || r.Status == "error")) {
		return r, errors.New("invalid host requests")
	}
	ids := map[string]bool{}
	for _, request := range r.Requests {
		if !namePattern.MatchString(request.ID) || request.Kind != "http" || !json.Valid(request.Payload) || ids[request.ID] {
			return r, errors.New("invalid host request")
		}
		ids[request.ID] = true
	}
	return r, nil
}
func apply(current json.RawMessage, r moduleResult) (json.RawMessage, error) {
	if r.Replace != nil {
		if len(r.Replace) > MaxValueBytes {
			return nil, errors.New("value limit")
		}
		return bytes.Clone(r.Replace), nil
	}
	var operations []json.RawMessage
	if err := json.Unmarshal(r.Patch, &operations); err != nil || operations == nil || len(operations) > MaxPatchOperations {
		return nil, errors.New("invalid patch")
	}
	patch, err := jsonpatch.DecodePatch(r.Patch)
	if err != nil {
		return nil, err
	}
	options := jsonpatch.NewApplyOptions()
	options.SupportNegativeIndices = false
	options.AllowMissingPathOnRemove = false
	options.EnsurePathExistsOnAdd = false
	options.AccumulatedCopySizeLimit = MaxValueBytes
	candidate, err := patch.ApplyWithOptions(bytes.Clone(current), options)
	if err != nil {
		return nil, err
	}
	if len(candidate) > MaxValueBytes {
		return nil, errors.New("value limit")
	}
	return candidate, nil
}
