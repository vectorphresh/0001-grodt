//go:build unix

package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vectorphresh/0001-grodt/internal/agent"
	"github.com/vectorphresh/0001-grodt/internal/runtime"
	"github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/tools"
	"github.com/vectorphresh/0001-grodt/internal/validation"
)

const inputSchema = `{"type":"object","required":["n"],"properties":{"n":{"type":"integer"}},"additionalProperties":false}`
const outputSchema = `{"type":"object","required":["arguments"],"properties":{"arguments":{"type":"object"}},"additionalProperties":false}`

// This is an actual stdio subprocess speaking JSON-RPC, not an external service.
// Raw frames let tests exercise malformed peers, pagination, and cancellation.
func TestMCPHelper(t *testing.T) {
	if os.Getenv("GRODT_TEST_MCP_CHILD") != "1" {
		return
	}
	mode := os.Getenv("GRODT_TEST_MCP_MODE")
	if path := os.Getenv("GRODT_TEST_MCP_PID"); path != "" {
		_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0600)
	}
	if mode == "startup-hang" {
		for {
			time.Sleep(time.Second)
		}
	}
	if mode == "descendant" {
		cmd := exec.Command("sleep", "300")
		if cmd.Start() != nil {
			os.Exit(4)
		}
		_ = os.WriteFile(os.Getenv("GRODT_TEST_MCP_DESCENDANT"), []byte(strconv.Itoa(cmd.Process.Pid)), 0600)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(2)
		}
		if req.Method == "notifications/cancelled" {
			_ = os.WriteFile(os.Getenv("GRODT_TEST_MCP_CANCELED"), []byte("canceled"), 0600)
			continue
		}
		if len(req.ID) == 0 {
			continue
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			reply["result"] = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "local-read-only-test", "version": "1"}}
		case "tools/list":
			if mode == "malformed" {
				fmt.Fprintln(os.Stdout, "not-json")
				continue
			}
			var params struct {
				Cursor string `json:"cursor"`
			}
			_ = json.Unmarshal(req.Params, &params)
			tool := map[string]any{"name": "read", "description": "Read test data", "inputSchema": json.RawMessage(inputSchema), "outputSchema": json.RawMessage(outputSchema)}
			if mode == "single" {
				reply["result"] = map[string]any{"tools": []any{tool}}
			} else if params.Cursor == "" {
				reply["result"] = map[string]any{"tools": []any{tool}, "nextCursor": "page2"}
			} else {
				if mode != "duplicate" {
					tool = map[string]any{"name": "text", "description": "Read text", "inputSchema": json.RawMessage(`{"type":"object"}`)}
				}
				result := map[string]any{"tools": []any{tool}}
				if mode == "repeat-cursor" {
					result["tools"] = []any{}
					result["nextCursor"] = "page2"
				}
				reply["result"] = result
			}
		case "tools/call":
			if mode == "disconnect" {
				os.Exit(3)
			}
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &params)
			if path := os.Getenv("GRODT_TEST_MCP_CALLS"); path != "" {
				f, _ := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
				if f != nil {
					_, _ = f.Write(append(req.Params, '\n'))
					_ = f.Close()
				}
			}
			switch mode {
			case "hang":
				continue
			case "tool-error":
				reply["result"] = map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "sensitive-server-result"}}}
			case "invalid-params":
				reply["error"] = map[string]any{"code": -32602, "message": "sensitive-server-result"}
			case "protocol-error":
				reply["error"] = map[string]any{"code": -32603, "message": "sensitive-server-result"}
			default:
				if params.Name == "text" {
					reply["result"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": "plain text"}}}
				} else {
					payload := map[string]any{"arguments": params.Arguments}
					reply["result"] = map[string]any{"structuredContent": payload, "content": []any{map[string]any{"type": "text", "text": "fallback must not replace structured content"}}}
				}
			}
		default:
			reply["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		if encoder.Encode(reply) != nil {
			os.Exit(5)
		}
	}
	os.Exit(0)
}

func helperConfig(t *testing.T, mode string) Config {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	return Config{Name: "test", Command: binary, Args: []string{"-test.run=^TestMCPHelper$"}, StartupTimeout: "5s", Env: map[string]string{
		"GRODT_TEST_MCP_CHILD": "1", "GRODT_TEST_MCP_MODE": mode,
		"GRODT_TEST_MCP_PID": filepath.Join(dir, "pid"), "GRODT_TEST_MCP_CALLS": filepath.Join(dir, "calls"),
		"GRODT_TEST_MCP_CANCELED": filepath.Join(dir, "canceled"), "GRODT_TEST_MCP_DESCENDANT": filepath.Join(dir, "descendant"),
	}}
}
func connectHelper(t *testing.T, ctx context.Context, mode string) (*Provider, Config) {
	t.Helper()
	config := helperConfig(t, mode)
	p, err := Connect(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p, config
}
func waitFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return data
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("helper event not observed")
	return nil
}
func assertReaped(t *testing.T, p *Provider) {
	t.Helper()
	if p.command.ProcessState == nil {
		t.Fatal("child not waited/reaped")
	}
	if err := p.command.Process.Signal(syscall.Signal(0)); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("child still live: %v", err)
	}
}
func sameJSON(a, b []byte) bool {
	var x, y any
	dx := json.NewDecoder(strings.NewReader(string(a)))
	dx.UseNumber()
	dy := json.NewDecoder(strings.NewReader(string(b)))
	dy.UseNumber()
	return dx.Decode(&x) == nil && dy.Decode(&y) == nil && reflect.DeepEqual(x, y)
}

func TestDiscoveryRegistrationAndRouting(t *testing.T) {
	p, config := connectHelper(t, context.Background(), "normal")
	registry := tools.NewRegistry()
	if err := registry.Register(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	definitions, err := registry.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 2 || definitions[0].Name != "test.read" || definitions[1].Name != "test.text" {
		t.Fatalf("definitions: %+v", definitions)
	}
	if !sameJSON(definitions[0].InputSchema, []byte(inputSchema)) || !sameJSON(definitions[0].OutputSchema, []byte(outputSchema)) || len(definitions[1].OutputSchema) != 0 {
		t.Fatal("schemas not preserved")
	}
	definitions[0].InputSchema[0] = '!'
	cached, err := p.Discover(context.Background())
	if err != nil || !json.Valid(cached[0].InputSchema) {
		t.Fatal("discovery snapshot alias")
	}
	// Preserve the exact number, avoiding a float64 argument round-trip.
	arguments := json.RawMessage(`{"n":9007199254740993}`)
	result, err := registry.Execute(context.Background(), tools.ToolCall{Name: "test.read", Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if result.Tool != "test.read" {
		t.Fatal("public result name not restored")
	}
	var forwarded struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(waitFile(t, config.Env["GRODT_TEST_MCP_CALLS"]), &forwarded); err != nil {
		t.Fatal(err)
	}
	if forwarded.Name != "read" || string(forwarded.Arguments) != string(arguments) {
		t.Fatalf("forwarded arguments: %s", forwarded.Arguments)
	}
	if err := tools.ValidateSchema(definitions[0].OutputSchema, result.Data); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	assertReaped(t, p)
	if err := p.Close(); err != nil {
		t.Fatal("second close failed", err)
	}
	if _, err := p.Execute(context.Background(), tools.ToolCall{Name: "read"}); err == nil {
		t.Fatal("closed provider executed")
	}
}

func TestDiscoveryOneTool(t *testing.T) {
	p, _ := connectHelper(t, context.Background(), "single")
	defs, err := p.Discover(context.Background())
	if err != nil || len(defs) != 1 {
		t.Fatalf("discovery: %d %v", len(defs), err)
	}
}

func TestDiscoveryCollisionsAndFailures(t *testing.T) {
	for _, mode := range []string{"duplicate", "repeat-cursor", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			p, _ := connectHelper(t, context.Background(), mode)
			registry := tools.NewRegistry()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := registry.Register(ctx, p); err == nil {
				t.Fatal("invalid discovery accepted")
			}
			defs, err := registry.List(context.Background())
			if err != nil || len(defs) != 0 {
				t.Fatal("partial registration")
			}
		})
	}
	p, _ := connectHelper(t, context.Background(), "normal")
	registry := tools.NewRegistry()
	if err := registry.Register(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(context.Background(), p); err == nil {
		t.Fatal("duplicate provider allowed")
	}
}

func TestMCPErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		mode  string
		fatal bool
	}{{"tool-error", false}, {"invalid-params", false}, {"protocol-error", true}, {"disconnect", true}} {
		t.Run(tc.mode, func(t *testing.T) {
			p, _ := connectHelper(t, context.Background(), tc.mode)
			_, err := p.Execute(context.Background(), tools.ToolCall{Name: "read", Arguments: json.RawMessage(`{"n":1}`)})
			if err == nil {
				t.Fatal("error ignored")
			}
			var fatal *tools.FatalError
			if errors.As(err, &fatal) != tc.fatal {
				t.Fatalf("classification: %v", err)
			}
			if strings.Contains(err.Error(), "sensitive-server-result") {
				t.Fatal("sensitive error content exposed")
			}
		})
	}
}

func TestRecoverableFailureBecomesObservation(t *testing.T) {
	p, _ := connectHelper(t, context.Background(), "tool-error")
	registry := tools.NewRegistry()
	if err := registry.Register(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	decider := agent.NewFakeAgent([]agent.Decision{{ToolCall: &tools.ToolCall{Name: "test.read", Arguments: json.RawMessage(`{"n":1}`)}}, {Done: true}})
	runner := runtime.NewRuntime(agent.RunSpec{}, state.NewMemoryStore(), decider, registry, validation.NewValidators("test.read"))
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	obs := decider.Inputs[1].Observation
	if obs == nil || obs.Error == nil || obs.Error.Type != "tool_execution_error" {
		t.Fatalf("observation: %+v", obs)
	}
}

func TestCallCancellationSendsMCPNotification(t *testing.T) {
	p, config := connectHelper(t, context.Background(), "hang")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := p.Execute(ctx, tools.ToolCall{Name: "read", Arguments: json.RawMessage(`{"n":1}`)})
		result <- err
	}()
	waitFile(t, config.Env["GRODT_TEST_MCP_CALLS"])
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("call did not cancel")
	}
	waitFile(t, config.Env["GRODT_TEST_MCP_CANCELED"])
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	assertReaped(t, p)
}

func TestLifetimeCancellationAndConcurrentClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, _ := connectHelper(t, ctx, "normal")
	cancel()
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = p.Close() }()
	}
	wg.Wait()
	assertReaped(t, p)
}

func TestStartupCancellationReapsChild(t *testing.T) {
	config := helperConfig(t, "startup-hang")
	config.StartupTimeout = "500ms"
	_, err := Connect(context.Background(), config)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startup error: %v", err)
	}
	pid, _ := strconv.Atoi(string(waitFile(t, config.Env["GRODT_TEST_MCP_PID"])))
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("startup process still exists: %v", err)
	}
}

func TestProcessDescendantsAreTerminated(t *testing.T) {
	p, config := connectHelper(t, context.Background(), "descendant")
	pid, _ := strconv.Atoi(string(waitFile(t, config.Env["GRODT_TEST_MCP_DESCENDANT"])))
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	assertReaped(t, p)
	// On Linux a killed orphan may briefly remain a zombie until PID 1 reaps it;
	// it is no longer a running process. Avoid treating that as an executing child.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err == nil && strings.Contains(string(stat), ") Z ") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("descendant still running")
}

func TestNormalizeResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result *mcp.CallToolResult
		want   string
	}{
		{"structured", &mcp.CallToolResult{StructuredContent: map[string]any{"status": "ok"}, Content: []mcp.Content{&mcp.TextContent{Text: "ignored"}}}, `{"status":"ok"}`},
		{"json text", &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: ` {"status":"ok"} `}}}, `{"status":"ok"}`},
		{"plain text", &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "hello"}}}, `{"content":[{"type":"text","text":"hello"}]}`},
		{"multiple blocks", &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "one"}, &mcp.TextContent{Text: "two"}}}, `{"content":[{"type":"text","text":"one"},{"type":"text","text":"two"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := normalize(tc.result)
			if err != nil || !sameJSON(data, []byte(tc.want)) {
				t.Fatalf("normalization: %s %v", data, err)
			}
		})
	}
}

func TestEnvironmentDoesNotForwardUnselectedCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "private-key")
	t.Setenv("SELECTED_CREDENTIAL", "selected-value")
	config := Config{InheritEnv: []string{"SELECTED_CREDENTIAL"}, Env: map[string]string{"OVERRIDE": "value"}}
	env := strings.Join(config.environment(), "\n")
	if strings.Contains(env, "private-key") || !strings.Contains(env, "SELECTED_CREDENTIAL=selected-value") || !strings.Contains(env, "OVERRIDE=value") {
		t.Fatal("environment selection violated")
	}
}

func TestDeniedToolNeverReachesMCPServer(t *testing.T) {
	p, config := connectHelper(t, context.Background(), "normal")
	registry := tools.NewRegistry()
	if err := registry.Register(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	decider := agent.NewFakeAgent([]agent.Decision{
		{ToolCall: &tools.ToolCall{Name: "test.text", Arguments: json.RawMessage(`{}`)}}, {Done: true},
	})
	runner := runtime.NewRuntime(agent.RunSpec{}, state.NewMemoryStore(), decider, registry, validation.NewValidators("test.read"))
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if decider.Inputs[1].Observation.Error.Type != "permission_denied" {
		t.Fatal("tool permission was not enforced")
	}
	if _, err := os.Stat(config.Env["GRODT_TEST_MCP_CALLS"]); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("denied tool reached server")
	}
}
