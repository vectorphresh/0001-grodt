package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vectorphresh/0001-grodt/internal/tools"
)

// Error reports only adapter diagnostics, never raw server error text/results,
// process arguments, stderr, or credential-bearing environment values.
type Error struct {
	Operation string
	Code      int64
	cause     error
}

func (e *Error) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("MCP %s (JSON-RPC code %d)", e.Operation, e.Code)
	}
	return "MCP " + e.Operation
}
func (e *Error) Unwrap() error { return e.cause }
func fatal(operation string, cause error) error {
	return &tools.FatalError{Err: &Error{Operation: operation, cause: cause}}
}

// Provider owns a single SDK stdio session. Close is idempotent and waits for
// the child to be reaped. Canceling the connection's context also closes it.
type Provider struct {
	name        string
	session     *mcp.ClientSession
	command     *exec.Cmd
	lifetime    context.Context
	cancel      context.CancelFunc
	closed      atomic.Bool
	closeOnce   sync.Once
	closeErr    error
	stopWatch   func() bool
	discoveryMu sync.Mutex
	definitions []tools.ToolDefinition
	discovered  bool
}

var _ tools.Provider = (*Provider)(nil)

func Connect(ctx context.Context, config Config) (*Provider, error) {
	timeout, err := config.timeout()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command(config.Command, config.Args...)
	cmd.Dir = config.Dir
	cmd.Env = config.environment()
	// nil Stderr is /dev/null: the server cannot leak credentials through our CLI.
	if err := isolateProcess(cmd); err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "grodt", Version: "0.1.0"}, &mcp.ClientOptions{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
	})
	startup, cancelStartup := context.WithTimeout(ctx, timeout)
	defer cancelStartup()
	session, err := client.Connect(startup, &mcp.CommandTransport{Command: cmd, TerminateDuration: time.Second}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		_ = cleanupProcessGroup(cmd)
		if startup.Err() != nil {
			return nil, startup.Err()
		}
		return nil, fatal("connect failed", err)
	}
	lifetime, cancel := context.WithCancel(context.Background())
	p := &Provider{name: config.Name, session: session, command: cmd, lifetime: lifetime, cancel: cancel}
	// Install the watcher only after all fields are initialized. Its callback
	// doesn't read stopWatch; only the caller-facing Close method does.
	p.stopWatch = context.AfterFunc(ctx, func() { p.close() })
	if err := ctx.Err(); err != nil {
		_ = p.Close()
		return nil, err
	}
	return p, nil
}

func (p *Provider) Name() string { return p.name }

func (p *Provider) Close() error {
	if p.stopWatch != nil {
		p.stopWatch()
	}
	return p.close()
}
func (p *Provider) close() error {
	p.closeOnce.Do(func() {
		p.closed.Store(true)
		p.cancel()
		err := p.session.Close()
		groupErr := cleanupProcessGroup(p.command)
		if err != nil {
			p.closeErr = &Error{Operation: "session close failed", cause: err}
		}
		if groupErr != nil {
			p.closeErr = &Error{Operation: "process group cleanup failed", cause: groupErr}
		}
	})
	return p.closeErr
}

func (p *Provider) callContext(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if p.closed.Load() {
		return nil, nil, fatal("session is closed", nil)
	}
	call, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(p.lifetime, cancel)
	return call, func() { stop(); cancel() }, nil
}

func classifyCallError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var rpc *jsonrpc.Error
	if errors.As(err, &rpc) && rpc.Code == jsonrpc.CodeInvalidParams {
		return &Error{Operation: "tool arguments rejected", Code: rpc.Code}
	}
	return fatal("connection or protocol failure", err)
}
