package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/openai"
)

type terminalStatus struct {
	writer io.Writer
	trace  bool
	key    string
}

func (s *terminalStatus) log(format string, args ...any) error {
	if s == nil {
		return nil
	}
	return s.write("[grodt] " + fmt.Sprintf(format, args...) + "\n")
}
func (s *terminalStatus) write(message string) error {
	if s == nil {
		return nil
	}
	if s.key != "" {
		encoded, _ := json.Marshal(s.key)
		message = strings.ReplaceAll(message, string(encoded[1:len(encoded)-1]), "[redacted]")
		message = strings.ReplaceAll(message, s.key, "[redacted]")
	}
	if _, err := io.WriteString(s.writer, message); err != nil {
		return fmt.Errorf("write diagnostic: %w", err)
	}
	return nil
}

type runMetrics struct {
	Requests         uint64
	UsageRequests    uint64
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

func (m *runMetrics) add(u *openai.Usage) {
	if u == nil {
		return
	}
	m.UsageRequests++
	m.PromptTokens += u.PromptTokens
	m.CompletionTokens += u.CompletionTokens
	m.TotalTokens += u.TotalTokens
}
func reportRun(s *terminalStatus, objective, outcome, rationale string, m runMetrics) error {
	if rationale == "" {
		rationale = "unavailable"
	}
	report := fmt.Sprintf("Objective: %s\nStatus: %s\nRationale: %s\nRequests: %d\n", objective, outcome, rationale, m.Requests)
	if m.UsageRequests == 0 {
		report += "Prompt tokens: unavailable\nCompletion tokens: unavailable\nTotal tokens: unavailable\n"
	} else {
		suffix := ""
		if m.UsageRequests < m.Requests {
			suffix = " (known subtotal)"
		}
		report += fmt.Sprintf("Prompt tokens: %d%s\nCompletion tokens: %d%s\nTotal tokens: %d%s\n", m.PromptTokens, suffix, m.CompletionTokens, suffix, m.TotalTokens, suffix)
	}
	report += fmt.Sprintf("Usage coverage: %d/%d requests\n", m.UsageRequests, m.Requests)
	return s.write(report)
}

// Counts client calls, not retries or estimated network attempts. One instance is
// shared by generation and evaluation; all calls are synchronous.
type observedClient struct {
	openai.Client
	status  *terminalStatus
	metrics *runMetrics
}

func (c observedClient) Prompt(ctx context.Context, prompt string) (openai.TextResult, error) {
	if c.status != nil && c.status.trace {
		if err := c.status.log("Outgoing generation prompt:\n%s\n--- end prompt ---", prompt); err != nil {
			return openai.TextResult{}, err
		}
	}
	if err := c.status.log("Generation request in progress..."); err != nil {
		return openai.TextResult{}, err
	}
	c.metrics.Requests++
	start := time.Now()
	result, err := c.Client.Prompt(ctx, prompt)
	if err == nil {
		c.metrics.add(result.Usage)
	}
	if logErr := c.finished("Generation", start, err); logErr != nil {
		return openai.TextResult{}, logErr
	}
	return result, err
}
func (c observedClient) RequestMutation(ctx context.Context, instructions string, state, observation json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
	if c.status != nil && c.status.trace {
		if err := c.status.log("Outgoing evaluation:\nInstructions: %s\nState: %s\nObservation: %s\nSchema: %s", instructions, state, observation, spec.Schema); err != nil {
			return openai.JSONResult{}, err
		}
	}
	if err := c.status.log("Evaluation request in progress..."); err != nil {
		return openai.JSONResult{}, err
	}
	c.metrics.Requests++
	start := time.Now()
	result, err := c.Client.RequestMutation(ctx, instructions, state, observation, spec)
	if err == nil {
		c.metrics.add(result.Usage)
	}
	if logErr := c.finished("Evaluation", start, err); logErr != nil {
		return openai.JSONResult{}, logErr
	}
	return result, err
}
func (c observedClient) finished(operation string, start time.Time, err error) error {
	outcome := "completed"
	if err != nil {
		outcome = "failed"
	}
	return c.status.log("%s request %s after %s.", operation, outcome, time.Since(start).Round(time.Millisecond))
}
