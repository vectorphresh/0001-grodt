package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/vectorphresh/0001-grodt/internal/config"
	"github.com/vectorphresh/0001-grodt/internal/loop"
	"github.com/vectorphresh/0001-grodt/internal/openai"
)

const defaultObjective = "Complete the user's request using the information supplied in this run."
const maxCycles = 20
const usage = "usage: grodt [--trace] [--config path] <prompt>"

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
	trace := flags.Bool("trace", false, "show generation and evaluation inputs")
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
	client, key, err := configuredClient(*path)
	if err != nil {
		return err
	}
	status := &terminalStatus{writer: diagnostics, trace: *trace, key: key}
	return execute(ctx, defaultObjective, flags.Arg(0), client, output, status)
}

func configuredClient(path string) (openai.Client, string, error) {
	resolver, err := config.Load(path)
	if err != nil {
		return nil, "", err
	}
	base, err := resolver.GetEnvironment("openai", "OPENAI_BASE_URL")
	if err != nil {
		return nil, "", err
	}
	key, err := resolver.GetEnvironment("openai", "OPENAI_API_KEY")
	if err != nil {
		return nil, "", err
	}
	client, err := openai.NewClient(openai.Config{BaseURL: base, APIKey: key})
	return client, key, err
}

// execute owns metrics and composes the same observed client into both operations.
func execute(ctx context.Context, objective, initial string, client openai.Client, output io.Writer, status *terminalStatus) error {
	metrics := runMetrics{}
	observed := observedClient{Client: client, status: status, metrics: &metrics}
	providers := []loop.Provider{loop.NewGenericProvider(observed)}
	return runObjective(ctx, objective, initial, providers, observed, output, status, &metrics)
}
