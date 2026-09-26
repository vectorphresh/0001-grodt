package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type Registry interface {
	List(context.Context) ([]ToolDefinition, error)
	Get(context.Context, string) (ToolDefinition, bool)
	Execute(context.Context, ToolCall) (ToolResult, error)
}

type registeredTool struct {
	definition ToolDefinition
	provider   Provider
	remoteName string
}

// ProviderRegistry takes a discovery snapshot at registration and routes public
// provider.tool names to the provider's original remote names.
type ProviderRegistry struct {
	mu        sync.RWMutex
	providers map[string]bool
	tools     map[string]registeredTool
}

func NewRegistry() *ProviderRegistry {
	return &ProviderRegistry{providers: make(map[string]bool), tools: make(map[string]registeredTool)}
}

func cloneDefinition(d ToolDefinition) ToolDefinition {
	d.InputSchema = append([]byte(nil), d.InputSchema...)
	d.OutputSchema = append([]byte(nil), d.OutputSchema...)
	return d
}

// Register is atomic: discovery, schema, or collision failures register nothing.
func (r *ProviderRegistry) Register(ctx context.Context, provider Provider) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if provider == nil {
		return fmt.Errorf("nil provider")
	}
	name := provider.Name()
	if strings.TrimSpace(name) != name || name == "" || strings.Contains(name, ".") {
		return fmt.Errorf("invalid provider name %q", name)
	}
	definitions, err := provider.Discover(ctx)
	if err != nil {
		return fmt.Errorf("discover %s: %w", name, err)
	}
	pending := make(map[string]registeredTool)
	for _, def := range definitions {
		remote := def.Name
		if remote == "" || strings.TrimSpace(remote) != remote {
			return fmt.Errorf("invalid remote tool name %q", remote)
		}
		def.Name = name + "." + remote
		def.Source = name
		if _, exists := pending[def.Name]; exists {
			return fmt.Errorf("duplicate tool %q", def.Name)
		}
		for _, schema := range [][]byte{def.InputSchema, def.OutputSchema} {
			if len(schema) > 0 {
				if _, err := compileSchema(schema); err != nil {
					return fmt.Errorf("schema for %s: %w", def.Name, err)
				}
			}
		}
		pending[def.Name] = registeredTool{cloneDefinition(def), provider, remote}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers[name] {
		return fmt.Errorf("duplicate provider %q", name)
	}
	for name := range pending {
		if _, exists := r.tools[name]; exists {
			return fmt.Errorf("duplicate tool %q", name)
		}
	}
	r.providers[name] = true
	for name, tool := range pending {
		r.tools[name] = tool
	}
	return nil
}

func (r *ProviderRegistry) List(ctx context.Context) ([]ToolDefinition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	definitions := make([]ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		definitions = append(definitions, cloneDefinition(tool.definition))
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	return definitions, nil
}

func (r *ProviderRegistry) Get(ctx context.Context, name string) (ToolDefinition, bool) {
	if ctx.Err() != nil {
		return ToolDefinition{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, exists := r.tools[name]
	return cloneDefinition(tool.definition), exists
}

func (r *ProviderRegistry) Execute(ctx context.Context, call ToolCall) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	r.mu.RLock()
	tool, exists := r.tools[call.Name]
	r.mu.RUnlock()
	if !exists {
		return ToolResult{}, fmt.Errorf("unknown tool %q", call.Name)
	}
	publicName := call.Name
	call.Name = tool.remoteName
	call.Arguments = append([]byte(nil), call.Arguments...)
	result, err := tool.provider.Execute(ctx, call)
	if err != nil {
		return ToolResult{}, err
	}
	result.Tool = publicName
	result.Data = append([]byte(nil), result.Data...)
	return result, nil
}

// FatalError marks a provider failure that cannot be corrected by the agent.
// Ordinary provider errors become observations; cancellation always stops a run.
type FatalError struct{ Err error }

func (e *FatalError) Error() string { return e.Err.Error() }
func (e *FatalError) Unwrap() error { return e.Err }
