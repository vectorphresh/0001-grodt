package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/vectorphresh/0001-grodt/internal/agent"
	"github.com/vectorphresh/0001-grodt/internal/llm"
	"github.com/vectorphresh/0001-grodt/internal/mcpclient"
	"github.com/vectorphresh/0001-grodt/internal/runtime"
	"github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/tools"
	"github.com/vectorphresh/0001-grodt/internal/validation"
)

// acceptanceRegistry records the actual provider boundary without modifying
// runtime or provider semantics. Only the operator's one reviewed tool is allowed.
type acceptanceRegistry struct {
	tools.Registry
	allowed string
	calls   []tools.ToolCall
	result  tools.ToolResult
}

func (r *acceptanceRegistry) Execute(ctx context.Context, call tools.ToolCall) (tools.ToolResult, error) {
	if call.Name != r.allowed || len(r.calls) != 0 {
		return tools.ToolResult{}, &tools.FatalError{Err: fmt.Errorf("MCP acceptance permits exactly one call to the selected tool")}
	}
	r.calls = append(r.calls, call)
	result, err := r.Registry.Execute(ctx, call)
	r.result = result
	return result, err
}

// runMCP uses the normal LLM agent/runtime. All server and tool selection is
// command configuration. Success is reported only after explicit session close.
func runMCP(ctx context.Context, llmConfig llm.OpenAIConfig, config mcpclient.Config, remoteTool, goal string, output io.Writer) (returnErr error) {
	if remoteTool == "" || goal == "" {
		return fmt.Errorf("MCP acceptance requires -mcp-tool and -goal for one reviewed read-only tool")
	}
	client, err := llm.NewOpenAIClient(llmConfig)
	if err != nil {
		return err
	}
	provider, err := mcpclient.Connect(ctx, config)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, provider.Close()) }()
	registry := tools.NewRegistry()
	if err := registry.Register(ctx, provider); err != nil {
		return err
	}
	definitions, err := registry.List(ctx)
	if err != nil {
		return err
	}
	name := provider.Name() + "." + remoteTool
	definition, exists := registry.Get(ctx, name)
	if !exists {
		return fmt.Errorf("configured read-only tool was not discovered")
	}
	store := state.NewMemoryStore()
	spec := agent.RunSpec{Goal: goal, Instructions: fmt.Sprintf("Use only %s exactly once, with arguments matching its discovered schema. After receiving a successful observation, return done:true. Do not change state or invoke any other tool.", name)}
	boundary := &acceptanceRegistry{Registry: registry, allowed: name}
	recorded := &acceptanceClient{client: client}
	runner := runtime.NewRuntime(spec, store, agent.NewLLMAgent(recorded), boundary, validation.NewValidators(name))
	if err := runner.Run(ctx); err != nil {
		// Feedback categories are runtime-owned. Do not print remote error text,
		// observation data, or model responses when acceptance fails.
		feedback := "none"
		if len(recorded.inputs) > 0 {
			obs := recorded.inputs[len(recorded.inputs)-1].Observation
			if obs != nil {
				feedback = "successful_observation"
				if obs.Error != nil {
					feedback = obs.Error.Type
				}
			}
		}
		return fmt.Errorf("MCP acceptance failed after %d provider call(s); latest feedback: %s: %w", len(boundary.calls), feedback, err)
	}
	if len(recorded.inputs) != 2 || len(boundary.calls) != 1 || boundary.calls[0].Name != name {
		return fmt.Errorf("MCP acceptance failed: expected two LLM requests and exactly one selected tool execution")
	}
	found := false
	for _, tool := range recorded.inputs[0].Tools {
		if tool.Name == name {
			// Compare against the actual discovered definition, not a hard-coded schema.
			advertised, _ := json.Marshal(tool)
			discovered, _ := json.Marshal(definition)
			found = bytes.Equal(advertised, discovered)
		}
	}
	if !found || recorded.inputs[0].Observation != nil {
		return fmt.Errorf("MCP acceptance failed: discovered tool definition missing from first LLM request")
	}
	obs := recorded.inputs[1].Observation
	if obs == nil || obs.Error != nil || obs.Tool != name || obs.Source != provider.Name() || !equalJSON(obs.Data, boundary.result.Data) {
		return fmt.Errorf("MCP acceptance failed: second LLM request lacks the executed tool observation")
	}
	snapshot, err := store.Load(ctx)
	if err != nil {
		return err
	}
	if snapshot.Version != 2 {
		return fmt.Errorf("MCP acceptance failed: expected two persisted steps")
	}
	if err := provider.Close(); err != nil {
		return err
	}
	fmt.Fprintf(output, "Discovered tools: %d\nSelected public tool: %s\nInput schema: %t; output schema: %t\n", len(definitions), name, len(definition.InputSchema) > 0, len(definition.OutputSchema) > 0)
	for i, criterion := range []string{
		"Grodt connected to the MCP server",
		"Tools were discovered dynamically",
		"Selected read-only tool exposed under its namespaced public name",
		"Real LLM received the discovered tool definition",
		"Real LLM requested that tool",
		"MCP provider executed the remote tool exactly once",
		"Result became a successful Observation",
		"Second LLM request contained that Observation",
		"LLM returned Done",
		"Runtime exited successfully and MCP resources closed cleanly",
	} {
		fmt.Fprintf(output, "PASS %d: %s\n", i+1, criterion)
	}
	return nil
}

// JSON embedded into a prompt can be reformatted/escaped without changing data.
func equalJSON(a, b []byte) bool {
	var left, right any
	x, y := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
	x.UseNumber()
	y.UseNumber()
	return x.Decode(&left) == nil && y.Decode(&right) == nil && reflect.DeepEqual(left, right)
}
