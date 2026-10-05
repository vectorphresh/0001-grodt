package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/config"
	"github.com/vectorphresh/0001-grodt/internal/loop"
	"github.com/vectorphresh/0001-grodt/internal/mcp"
	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/state/wasm"
	"github.com/vectorphresh/0001-grodt/internal/stateflow"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

const defaultObjective = "Complete the user's request using the information supplied in this run."
const maxCycles = 500
const usage = "usage: grodt [--trace] [--trace-dir path] [--config path] [--state-definition path] [--allow-state-http] <prompt>"

var errIncomplete = errors.New("objective incomplete: cycle limit reached")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, errIncomplete) {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(exitCode(err))
}

func exitCode(err error) int {
	if err == nil || errors.Is(err, context.Canceled) {
		return 0
	}
	if errors.Is(err, errIncomplete) {
		return 2
	}
	return 1
}

// run is the composition root. There is deliberately no stdin dependency.
func run(ctx context.Context, args []string, output, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("grodt", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	allowHTTP := flags.Bool("allow-state-http", false, "allow state modules to request HTTP work")
	statePath := flags.String("state-definition", "", "state partition definition file (overrides config)")
	trace := flags.Bool("trace", false, "show generation and evaluation inputs")
	traceDir := flags.String("trace-dir", "", "save inference requests, responses, and state snapshots")
	path := flags.String("config", "config.yaml", "configuration file")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(diagnostics, usage)
			return err
		}
		return errors.New(usage)
	}
	if flags.NArg() != 1 || strings.TrimSpace(flags.Arg(0)) == "" {
		return errors.New(usage)
	}
	resolver, err := config.Load(*path)
	if err != nil {
		return err
	}
	// Explicit flags, including an empty value, override the YAML selection.
	stateFlagSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "state-definition" {
			stateFlagSet = true
		}
	})
	if !stateFlagSet {
		*statePath = resolver.State().Definition
		if *statePath != "" && !filepath.IsAbs(*statePath) {
			*statePath = filepath.Join(filepath.Dir(*path), *statePath)
		}
	}
	client, key, err := clientFromResolver(resolver)
	if err != nil {
		return err
	}
	status := &terminalStatus{writer: diagnostics, trace: *trace, key: key}
	cfg := resolver.MCP()
	var capabilities *mcp.Runtime
	if len(cfg.Servers) > 0 {
		capabilities, err = mcp.New(ctx, cfg)
		if err != nil {
			return err
		}
		defer capabilities.Close()
	}
	var store *runstate.Store
	if *statePath == "" {
		store, err = runstate.New(ctx, nil, runstate.Options{AllowHTTP: *allowHTTP})
	} else {
		store, err = wasm.Load(ctx, *statePath, runstate.Options{AllowHTTP: *allowHTTP})
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("state initialization failed")
	}
	defer store.Close(context.Background())
	if *traceDir != "" {
		status.artifacts, err = newInferenceTrace(*traceDir, store, key)
		if err != nil {
			return err
		}
		if err := status.log("Trace artifacts: %s", status.artifacts.dir); err != nil {
			return err
		}
		defer status.saveArtifact("final-state.json", store.JSON())
	}
	return executeWithCapabilities(ctx, defaultObjective, flags.Arg(0), client, output, status, store, capabilities)
}

func configuredClient(path string) (openai.Client, string, error) {
	resolver, err := config.Load(path)
	if err != nil {
		return nil, "", err
	}
	return clientFromResolver(resolver)
}

func clientFromResolver(resolver *config.Resolver) (openai.Client, string, error) {
	base, err := resolver.GetEnvironment("openai", "OPENAI_BASE_URL")
	if err != nil {
		return nil, "", err
	}
	key, err := resolver.GetEnvironment("openai", "OPENAI_API_KEY")
	if err != nil {
		return nil, "", err
	}
	var timeout time.Duration
	configuredTimeout, err := resolver.GetEnvironment("openai", "OPENAI_TIMEOUT")
	if err == nil {
		timeout, err = time.ParseDuration(configuredTimeout)
		if err != nil || timeout <= 0 {
			return nil, "", errors.New("OPENAI_TIMEOUT must be a positive duration, such as 10m or 300s")
		}
	} else if !errors.Is(err, config.ErrNotFound) {
		return nil, "", err
	}
	model, err := resolver.GetEnvironment("openai", "OPENAI_MODEL")
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return nil, "", err
	}
	client, err := openai.NewClient(openai.Config{BaseURL: base, APIKey: key, Model: model, Timeout: timeout})
	return client, key, err
}

// execute owns metrics and composes the same observed client into both operations.
func execute(ctx context.Context, objective, initial string, client openai.Client, output io.Writer, status *terminalStatus) error {
	store, err := runstate.New(ctx, nil, runstate.Options{})
	if err != nil {
		return err
	}
	defer store.Close(context.Background())
	return executeWithState(ctx, objective, initial, client, output, status, store)
}

func executeWithState(ctx context.Context, objective, initial string, client openai.Client, output io.Writer, status *terminalStatus, store *runstate.Store) error {
	return executeWithCapabilities(ctx, objective, initial, client, output, status, store, nil)
}
func executeWithCapabilities(ctx context.Context, objective, initial string, client openai.Client, output io.Writer, status *terminalStatus, store *runstate.Store, capabilities *mcp.Runtime) error {
	metrics := runMetrics{}
	observed := observedClient{Client: client, status: status, metrics: &metrics}
	withState := &stateflow.Client{Client: observed, Store: store}
	providers := []loop.Provider{loop.NewGenericProvider(withState)}
	if capabilities != nil {
		if _, ok := client.(toolcall.Client); !ok {
			return errors.New("LLM client does not support tools")
		}
		providers = []loop.Provider{&loop.ToolProvider{Client: observed, Observer: withState, Evaluator: observed, Reconciler: observed, Runtime: capabilities, Store: store}}
	}
	return runObjective(ctx, objective, initial, providers, withState, output, status, &metrics, store)
}
