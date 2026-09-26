package tools

import (
	"context"
	"fmt"
)

// FakeProvider records remote calls and can simulate discovery/execution errors.
type FakeProvider struct {
	ProviderName  string
	Tools         []ToolDefinition
	Results       map[string]ToolResult
	Calls         []ToolCall
	DiscoverError error
	ExecuteError  error
}

func NewFakeProvider(definitions []ToolDefinition, results map[string]ToolResult) *FakeProvider {
	return &FakeProvider{Tools: definitions, Results: results}
}
func (p *FakeProvider) Name() string {
	if p.ProviderName != "" {
		return p.ProviderName
	}
	return "fake"
}
func (p *FakeProvider) Discover(ctx context.Context) ([]ToolDefinition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.Tools, p.DiscoverError
}
func (p *FakeProvider) Execute(ctx context.Context, call ToolCall) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	p.Calls = append(p.Calls, call)
	if p.ExecuteError != nil {
		return ToolResult{}, p.ExecuteError
	}
	result, exists := p.Results[call.Name]
	if !exists {
		return ToolResult{}, fmt.Errorf("tool %s has no fake result", call.Name)
	}
	return result, nil
}
