// Package wasm implements the import-free grodt.state/v1 ABI with fresh instances.
package wasm

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/vectorphresh/0001-grodt/internal/state"
)

const MaxModuleBytes = 8 << 20
const MemoryPages = 1024 // 64 MiB, with 64 KiB WebAssembly pages.
const ProcessTimeout = time.Second

type Module struct {
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
}

func failure(code string) error { return &state.RuntimeError{Code: code} }

// New compiles and verifies exports, version, and instantiation before returning.
// Every configured module must pass this before the run starts.
func New(ctx context.Context, binary []byte) (*Module, error) {
	if ctx == nil {
		return nil, errors.New("module context is required")
	}
	if len(binary) > MaxModuleBytes {
		return nil, failure("resource_limit")
	}
	r := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(MemoryPages).WithCloseOnContextDone(true).WithDebugInfoEnabled(false))
	compiled, err := r.CompileModule(ctx, binary)
	if err != nil {
		r.Close(context.Background())
		return nil, failure("abi_mismatch")
	}
	m := &Module{runtime: r, compiled: compiled}
	ok := false
	defer func() {
		if !ok {
			m.Close(context.Background())
		}
	}()
	if len(compiled.ImportedFunctions()) != 0 || len(compiled.ImportedMemories()) != 0 {
		return nil, failure("abi_mismatch")
	}
	for name, signature := range map[string]struct{ params, results []api.ValueType }{
		"grodt_state_abi_version": {nil, []api.ValueType{api.ValueTypeI32}},
		"grodt_alloc":             {[]api.ValueType{api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}},
		"grodt_process":           {[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}},
	} {
		f, exists := compiled.ExportedFunctions()[name]
		if !exists || !same(f.ParamTypes(), signature.params) || !same(f.ResultTypes(), signature.results) {
			return nil, failure("abi_mismatch")
		}
	}
	if _, exists := compiled.ExportedMemories()["memory"]; !exists {
		return nil, failure("abi_mismatch")
	}
	callCtx, cancel := context.WithTimeout(ctx, ProcessTimeout)
	defer cancel()
	instance, err := m.instantiate(callCtx)
	if err != nil {
		return nil, err
	}
	instance.Close(context.Background())
	ok = true
	return m, nil
}
func same(a, b []api.ValueType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func (m *Module) instantiate(ctx context.Context) (api.Module, error) {
	// No WASI or other host modules are installed. Any other imported global/table
	// also fails linking. Disable conventional exported start functions; a core
	// start section still executes under the same memory and deadline limits.
	instance, err := m.runtime.InstantiateModule(ctx, m.compiled, wazero.NewModuleConfig().WithName("").WithStartFunctions())
	if err != nil {
		if ctx.Err() != nil {
			return nil, failure("timeout")
		}
		return nil, failure("instantiate")
	}
	v, err := instance.ExportedFunction("grodt_state_abi_version").Call(ctx)
	if err != nil || len(v) != 1 || v[0] != 1 {
		instance.Close(context.Background())
		return nil, failure("abi_mismatch")
	}
	return instance, nil
}
func (m *Module) Process(ctx context.Context, current json.RawMessage, event state.Event) (json.RawMessage, error) {
	input, err := json.Marshal(struct {
		ABI     string          `json:"abi"`
		Current json.RawMessage `json:"current"`
		Event   state.Event     `json:"event"`
	}{state.ABI, current, event})
	if err != nil || len(input) > state.MaxInputBytes {
		return nil, failure("resource_limit")
	}
	callCtx, cancel := context.WithTimeout(ctx, ProcessTimeout)
	defer cancel()
	instance, err := m.instantiate(callCtx)
	if err != nil {
		return nil, err
	}
	defer instance.Close(context.Background())
	ptr, err := instance.ExportedFunction("grodt_alloc").Call(callCtx, uint64(len(input)))
	if err != nil {
		return nil, executionFailure(callCtx)
	}
	if !instance.Memory().Write(uint32(ptr[0]), input) {
		return nil, failure("resource_limit")
	}
	output, err := instance.ExportedFunction("grodt_process").Call(callCtx, ptr[0], uint64(len(input)))
	if err != nil {
		return nil, executionFailure(callCtx)
	}
	packed := output[0]
	offset, length := uint32(packed>>32), uint32(packed)
	if length > state.MaxValueBytes {
		return nil, failure("resource_limit")
	}
	data, ok := instance.Memory().Read(offset, length)
	if !ok {
		return nil, failure("malformed_response")
	}
	if !json.Valid(data) {
		return nil, failure("malformed_response")
	}
	return append(json.RawMessage(nil), data...), nil
}
func executionFailure(ctx context.Context) error {
	if ctx.Err() != nil {
		return failure("timeout")
	}
	return failure("trap")
}
func (m *Module) Close(ctx context.Context) error { return m.runtime.Close(ctx) }
