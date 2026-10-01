package wasm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/vectorphresh/0001-grodt/internal/state"
)

type manifest struct {
	Partitions []struct {
		Name    string `json:"name"`
		Schema  string `json:"schema"`
		Initial string `json:"initial"`
		Module  string `json:"module"`
		ABI     string `json:"abi"`
	} `json:"partitions"`
}

// Load uses an explicit JSON manifest. Resource paths resolve against its folder;
// there is no environment interpolation or credential access.
func Load(ctx context.Context, path string, options state.Options) (*state.Store, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open state definition")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxModuleBytes+1))
	if err != nil || len(data) > MaxModuleBytes {
		return nil, errors.New("invalid or oversized state definition")
	}
	var doc manifest
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&doc); err != nil {
		return nil, errors.New("invalid state definition")
	}
	var extra any
	if doc.Partitions == nil || d.Decode(&extra) != io.EOF {
		return nil, errors.New("invalid state definition")
	}
	var definitions []state.Definition
	success := false
	defer func() {
		if !success {
			for _, d := range definitions {
				d.Module.Close(context.Background())
			}
		}
	}()
	read := func(name string, limit int) ([]byte, error) {
		if name == "" {
			return nil, errors.New("missing resource path")
		}
		p := name
		if !filepath.IsAbs(p) {
			p = filepath.Join(filepath.Dir(path), p)
		}
		f, err := os.Open(p)
		if err != nil {
			return nil, errors.New("cannot open state resource")
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, int64(limit+1)))
		if err != nil || len(b) > limit {
			return nil, errors.New("invalid or oversized state resource")
		}
		return b, nil
	}
	for _, entry := range doc.Partitions {
		if entry.ABI != state.ABI {
			return nil, errors.New("unsupported state ABI")
		}
		schema, err := read(entry.Schema, state.MaxValueBytes)
		if err != nil {
			return nil, err
		}
		initial, err := read(entry.Initial, state.MaxValueBytes)
		if err != nil {
			return nil, err
		}
		binary, err := read(entry.Module, MaxModuleBytes)
		if err != nil {
			return nil, err
		}
		module, err := New(ctx, binary)
		if err != nil {
			return nil, errors.New("state module configuration failed")
		}
		definitions = append(definitions, state.Definition{Name: entry.Name, Schema: schema, Initial: initial, Module: module})
	}
	store, err := state.New(ctx, definitions, options)
	if err != nil {
		return nil, err
	}
	success = true
	return store, nil
}
