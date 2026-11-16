package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/openai"
)

func TestRequestTimingWindows(t *testing.T) {
	now := time.Unix(1000, 0)
	m := requestTimings{windowStart: now}
	var output bytes.Buffer
	status := &terminalStatus{writer: &output}
	m.add(8*time.Second, false)
	m.add(12*time.Second, true)
	if err := m.flush(status, now.Add(59*time.Second), false); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatal("summary emitted before interval")
	}
	if err := m.flush(status, now.Add(time.Minute), false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "2 calls, average 10s, maximum 12s, 1 failed") {
		t.Fatal(output.String())
	}
	m.add(2*time.Second, false)
	if err := m.flush(status, now.Add(2*time.Minute), false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "1 calls, average 2s, maximum 2s, 0 failed") {
		t.Fatal("previous window carried over:", output.String())
	}
	previous := output.String()
	if err := m.flush(status, now.Add(3*time.Minute), false); err != nil {
		t.Fatal(err)
	}
	if output.String() != previous {
		t.Fatal("empty window produced output")
	}
}

func TestRepeatedRequestsAreQuietAndFinalReportFlushesTiming(t *testing.T) {
	for _, trace := range []bool{false, true} {
		var output bytes.Buffer
		status := &terminalStatus{writer: &output, trace: trace}
		metrics := &runMetrics{}
		client := observedClient{status: status, metrics: metrics, Client: testClient{
			prompt: func(context.Context, string) (openai.TextResult, error) {
				return openai.TextResult{}, nil
			},
		}}
		for range 3 {
			if _, err := client.Prompt(context.Background(), "test"); err != nil {
				t.Fatal(err)
			}
		}
		if strings.Count(output.String(), "request in progress") != 1 || strings.Contains(output.String(), "completed after") || strings.Contains(output.String(), "LLM request timing") {
			t.Fatal(output.String())
		}
		if err := reportRun(status, "test", "achieved", "done", *metrics); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "LLM request timing: 3 calls") {
			t.Fatal("final partial window missing:", output.String())
		}
	}
}

func TestRequestFailureIsLoggedImmediately(t *testing.T) {
	var output bytes.Buffer
	metrics := &runMetrics{timing: requestTimings{windowStart: time.Now()}}
	client := observedClient{status: &terminalStatus{writer: &output}, metrics: metrics}
	if err := client.finished("Tool-capable generation", time.Now().Add(-8*time.Second), errors.New("failure")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Tool-capable generation request failed after") || strings.Contains(output.String(), "LLM request timing") {
		t.Fatal(output.String())
	}
	if metrics.timing.failures != 1 {
		t.Fatal("failure not included in summary")
	}
}

func TestRequestTimingWriteFailureRetainsWindow(t *testing.T) {
	m := requestTimings{windowStart: time.Unix(1000, 0)}
	m.add(time.Second, false)
	if err := m.flush(&terminalStatus{writer: brokenWriter{}}, time.Unix(1060, 0), false); err == nil {
		t.Fatal("diagnostic write failure ignored")
	}
	if m.calls != 1 {
		t.Fatal("unreported timing lost")
	}
}
