package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

type terminalStatus struct {
	writer     io.Writer
	trace      bool
	key        string
	artifacts  *inferenceTrace
	milestones *milestoneReporter
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
	timing           requestTimings
}

const requestSummaryInterval = time.Minute

// Summaries are emitted at request boundaries, so a slow request can delay the
// next summary. Keep this synchronous with the existing client and diagnostics.
type requestTimings struct {
	windowStart time.Time
	calls       uint64
	failures    uint64
	total       time.Duration
	maximum     time.Duration
}

func (m *requestTimings) add(elapsed time.Duration, failed bool) {
	m.calls++
	m.total += elapsed
	m.maximum = max(m.maximum, elapsed)
	if failed {
		m.failures++
	}
}

func (m *requestTimings) flush(s *terminalStatus, now time.Time, final bool) error {
	if m.calls == 0 || (!final && now.Sub(m.windowStart) < requestSummaryInterval) {
		return nil
	}
	if err := s.log("LLM request timing: %d calls, average %s, maximum %s, %d failed.", m.calls,
		(m.total / time.Duration(m.calls)).Round(time.Millisecond), m.maximum.Round(time.Millisecond), m.failures); err != nil {
		return err
	}
	*m = requestTimings{windowStart: now}
	return nil
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
	if s != nil {
		if err := s.milestones.drain(); err != nil {
			return err
		}
	}
	if err := m.timing.flush(s, time.Now(), true); err != nil {
		return err
	}
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
	status     *terminalStatus
	metrics    *runMetrics
	milestones *milestoneReporter
}

func (c observedClient) promptOnce(ctx context.Context, prompt string) (openai.TextResult, error) {
	if err := c.milestones.drain(); err != nil {
		return openai.TextResult{}, err
	}
	if c.status != nil && c.status.trace {
		if err := c.status.log("Outgoing generation prompt:\n%s\n--- end prompt ---", prompt); err != nil {
			return openai.TextResult{}, err
		}
	}
	if err := c.started("Generation"); err != nil {
		return openai.TextResult{}, err
	}
	c.metrics.Requests++
	start := time.Now()
	c.traceRequest("generation", map[string]any{"prompt": prompt})
	result, err := c.Client.Prompt(ctx, prompt)
	c.traceResponse(result, start, err)
	if err == nil {
		c.metrics.add(result.Usage)
	}
	if logErr := c.finished("Generation", start, err); logErr != nil {
		return openai.TextResult{}, logErr
	}
	return result, err
}
func (c observedClient) requestMutationOnce(ctx context.Context, instructions string, state, observation json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
	if err := c.milestones.drain(); err != nil {
		return openai.JSONResult{}, err
	}
	if c.status != nil && c.status.trace {
		if err := c.status.log("Outgoing evaluation:\nInstructions: %s\nState: %s\nObservation: %s\nSchema: %s", instructions, state, observation, spec.Schema); err != nil {
			return openai.JSONResult{}, err
		}
	}
	if err := c.started("Evaluation"); err != nil {
		return openai.JSONResult{}, err
	}
	c.metrics.Requests++
	start := time.Now()
	c.traceRequest("evaluation", map[string]any{"instructions": instructions, "state": state, "observation": observation, "specification": spec})
	result, err := c.Client.RequestMutation(ctx, instructions, state, observation, spec)
	c.traceResponse(result, start, err)
	if err == nil {
		c.metrics.add(result.Usage)
	}
	if logErr := c.finished("Evaluation", start, err); logErr != nil {
		return openai.JSONResult{}, logErr
	}
	return result, err
}
func (c observedClient) started(operation string) error {
	if c.metrics.timing.windowStart.IsZero() {
		c.metrics.timing.windowStart = time.Now()
	}
	if c.metrics.Requests == 0 {
		return c.status.log("%s request in progress...", operation)
	}
	return nil
}

func (c observedClient) finished(operation string, start time.Time, err error) error {
	now := time.Now()
	elapsed := now.Sub(start)
	c.metrics.timing.add(elapsed, err != nil)
	if err != nil {
		if logErr := c.status.log("%s request failed after %s.", operation, elapsed.Round(time.Millisecond)); logErr != nil {
			return logErr
		}
	}
	return c.metrics.timing.flush(c.status, now, false)
}

// Structured generation uses the same accounting and redacted progress channel
// as ordinary generation. The provider owns validation/admission, not this wrapper.
func (c observedClient) promptWithSpecificationOnce(ctx context.Context, instructions, prompt string, input json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
	if err := c.milestones.drain(); err != nil {
		return openai.JSONResult{}, err
	}
	if err := c.started("Structured generation"); err != nil {
		return openai.JSONResult{}, err
	}
	c.metrics.Requests++
	start := time.Now()
	c.traceRequest("structured-generation", map[string]any{"instructions": instructions, "prompt": prompt, "input": input, "specification": spec})
	result, err := c.Client.PromptWithSpecification(ctx, instructions, prompt, input, spec)
	c.traceResponse(result, start, err)
	if err == nil {
		c.metrics.add(result.Usage)
	}
	if logErr := c.finished("Structured generation", start, err); logErr != nil {
		return openai.JSONResult{}, logErr
	}
	return result, err
}

func (c observedClient) generateWithToolsOnce(ctx context.Context, request toolcall.Request) (toolcall.Response, error) {
	if err := c.milestones.drain(); err != nil {
		return toolcall.Response{}, err
	}
	client, ok := c.Client.(toolcall.Client)
	if !ok {
		return toolcall.Response{}, errors.New("LLM client does not support tools")
	}
	if err := c.started("Tool-capable generation"); err != nil {
		return toolcall.Response{}, err
	}
	c.metrics.Requests++
	start := time.Now()
	c.traceRequest("tool-generation", request)
	result, err := client.GenerateWithTools(ctx, request)
	c.traceResponse(result, start, err)
	if err == nil && result.UsageAvailable {
		c.metrics.add(&openai.Usage{PromptTokens: result.PromptTokens, CompletionTokens: result.CompletionTokens, TotalTokens: result.TotalTokens})
	}
	if logErr := c.finished("Tool-capable generation", start, err); logErr != nil {
		return toolcall.Response{}, logErr
	}
	return result, err
}
