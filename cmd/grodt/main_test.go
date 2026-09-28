package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/config"
	"github.com/vectorphresh/0001-grodt/internal/openai"
)

type testClient struct {
	prompt   func(context.Context, string) (openai.TextResult, error)
	mutation func(context.Context, string, json.RawMessage, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error)
}

func (c testClient) Prompt(ctx context.Context, p string) (openai.TextResult, error) {
	return c.prompt(ctx, p)
}
func (c testClient) RequestMutation(ctx context.Context, i string, s, o json.RawMessage, j openai.JSONSpecification) (openai.JSONResult, error) {
	return c.mutation(ctx, i, s, o, j)
}
func (testClient) PromptWithSpecification(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
	panic("ordinary generation must use Prompt")
}
func evaluationResult(achieved bool, rationale string) openai.JSONResult {
	data, _ := json.Marshal(GoalEvaluation{achieved, rationale})
	return openai.JSONResult{JSON: data, Usage: &openai.Usage{PromptTokens: 20, CompletionTokens: 5, TotalTokens: 25}}
}
func configFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func unset(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "temporary")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluationDecode(t *testing.T) {
	for _, achieved := range []bool{false, true} {
		result, err := decodeEvaluation(evaluationResult(achieved, "reason").JSON)
		if err != nil || result.Achieved != achieved || result.Rationale != "reason" {
			t.Fatal("valid evaluation rejected")
		}
	}
	for _, data := range []string{``, `null`, `[]`, `{}`, `{"rationale":"reason"}`, `{"achieved":false}`, `{"achieved":null,"rationale":"reason"}`, `{"achieved":true,"rationale":null}`, `{"achieved":"true","rationale":"reason"}`, `{"achieved":true,"rationale":" "}`, `{"achieved":true,"rationale":"reason","extra":1}`, `{"achieved":true,"achieved":false,"rationale":"reason"}`, `{"Achieved":true,"rationale":"reason"}`, `{"achieved":true,"rationale":"reason"} {}`} {
		if _, err := decodeEvaluation([]byte(data)); err == nil {
			t.Errorf("invalid evaluation accepted: %s", data)
		}
	}
}

func TestAutonomousContinuationAndUsage(t *testing.T) {
	var calls []string
	generations := 0
	c := testClient{
		prompt: func(_ context.Context, p string) (openai.TextResult, error) {
			calls = append(calls, "generate")
			generations++
			if generations == 2 {
				for _, part := range []string{"Objective:\nOriginal goal", "Original request", "Previous response:\nDRAFT", "Evaluation rationale (temporary guidance):\nSupply final token", "subsequent attempt"} {
					if !strings.Contains(p, part) {
						t.Errorf("continuation missing %s", part)
					}
				}
			}
			text := "DRAFT"
			if generations == 2 {
				text = "FINAL"
			}
			return openai.TextResult{Text: text, Usage: &openai.Usage{PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13}}, nil
		},
		mutation: func(_ context.Context, instructions string, state, observation json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
			calls = append(calls, "evaluate")
			var data struct {
				Objective string   `json:"objective"`
				Initial   string   `json:"initial_prompt"`
				Context   []string `json:"context"`
				Response  string   `json:"current_response"`
			}
			if json.Unmarshal(state, &data) != nil || data.Objective != "Original goal" || data.Initial != "Original request" || data.Response == "" {
				t.Error("evaluation lost explicit run state")
			}
			if string(observation) != `{"purpose":"goal_evaluation"}` || !spec.Strict || string(spec.Schema) != evaluationSchema || instructions == "" {
				t.Error("incorrect evaluation translation")
			}
			if generations == 2 {
				return evaluationResult(true, "Done"), nil
			}
			return evaluationResult(false, "Supply final token"), nil
		},
	}
	var out, diag bytes.Buffer
	if err := execute(context.Background(), "Original goal", "Original request", c, &out, &terminalStatus{writer: &diag}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"generate", "evaluate", "generate", "evaluate"}) || out.String() != "FINAL\n" {
		t.Fatal("wrong sequencing or intermediate output")
	}
	for _, want := range []string{"Status: achieved", "Rationale: Done", "Requests: 4", "Prompt tokens: 60", "Completion tokens: 16", "Total tokens: 76", "Usage coverage: 4/4"} {
		if !strings.Contains(diag.String(), want) {
			t.Errorf("report missing %s", want)
		}
	}
}

func TestOneCycleAndCycleLimit(t *testing.T) {
	for _, complete := range []bool{true, false} {
		generations, evaluations := 0, 0
		c := testClient{
			prompt: func(context.Context, string) (openai.TextResult, error) {
				generations++
				return openai.TextResult{Text: "result"}, nil
			},
			mutation: func(context.Context, string, json.RawMessage, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
				evaluations++
				return evaluationResult(complete, "reason"), nil
			},
		}
		var out, diag bytes.Buffer
		err := execute(context.Background(), defaultObjective, "request", c, &out, &terminalStatus{writer: &diag})
		want := maxCycles
		status := "incomplete"
		code := 2
		if complete {
			want = 1
			status = "achieved"
			code = 0
		}
		if generations != want || evaluations != want || exitCode(err) != code || out.String() != "result\n" {
			t.Fatal("incorrect stopping condition")
		}
		if !strings.Contains(diag.String(), "Status: "+status) || !strings.Contains(diag.String(), "(known subtotal)") {
			t.Fatal("incorrect outcome or partial usage report")
		}
	}
}

func TestFailuresAbortWithoutInferenceOrRetry(t *testing.T) {
	for _, stage := range []string{"generation", "evaluation", "decode"} {
		for _, cause := range []error{context.DeadlineExceeded, context.Canceled, errors.New("private-sensitive-error")} {
			g, e := 0, 0
			c := testClient{
				prompt: func(context.Context, string) (openai.TextResult, error) {
					g++
					if stage == "generation" {
						return openai.TextResult{Text: "partial"}, cause
					}
					return openai.TextResult{Text: "result"}, nil
				},
				mutation: func(context.Context, string, json.RawMessage, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
					e++
					if stage == "decode" {
						return openai.JSONResult{JSON: json.RawMessage(`{}`)}, nil
					}
					return openai.JSONResult{}, cause
				},
			}
			var out, diag bytes.Buffer
			err := execute(context.Background(), defaultObjective, "request", c, &out, &terminalStatus{writer: &diag})
			if err == nil || g != 1 || e > 1 || (stage == "generation" && e != 0) || out.Len() != 0 {
				t.Fatal("failure continued or published partial response")
			}
			if stage != "decode" && (cause == context.Canceled || cause == context.DeadlineExceeded) && !errors.Is(err, cause) {
				t.Fatal("context identity lost")
			}
			if strings.Contains(diag.String(), "Status: achieved") || strings.Contains(diag.String(), "private-sensitive-error") || strings.Contains(err.Error(), "private-sensitive-error") {
				t.Fatal("unsafe failure reporting")
			}
		}
	}
}

func TestBlockingGenerationPreventsEvaluation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var evaluated atomic.Bool
	c := testClient{
		prompt: func(context.Context, string) (openai.TextResult, error) {
			close(entered)
			<-release
			return openai.TextResult{Text: "done"}, nil
		},
		mutation: func(context.Context, string, json.RawMessage, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
			evaluated.Store(true)
			return evaluationResult(true, "done"), nil
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- execute(context.Background(), defaultObjective, "request", c, io.Discard, &terminalStatus{writer: io.Discard})
	}()
	<-entered
	if evaluated.Load() {
		t.Error("evaluation overlapped generation")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestBlankInputRejected(t *testing.T) {
	for _, values := range [][2]string{{" ", "prompt"}, {"objective", " "}} {
		if err := execute(context.Background(), values[0], values[1], testClient{}, io.Discard, &terminalStatus{writer: io.Discard}); err == nil {
			t.Fatal("blank input accepted")
		}
	}
}

func TestUsageAbsenceAndZeros(t *testing.T) {
	for _, known := range []bool{false, true} {
		var diag bytes.Buffer
		m := runMetrics{Requests: 2}
		if known {
			m.add(&openai.Usage{})
			m.add(&openai.Usage{})
		}
		if err := reportRun(&terminalStatus{writer: &diag}, "goal", "achieved", "reason", m); err != nil {
			t.Fatal(err)
		}
		want := "Total tokens: unavailable"
		if known {
			want = "Total tokens: 0"
		}
		if !strings.Contains(diag.String(), want) {
			t.Fatal("unavailable usage confused with zero")
		}
	}
}

func serveCompletion(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": text}}}, "usage": map[string]int{"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}})
}

func TestCLIConfigurationAndTrace(t *testing.T) {
	key := "credential<" + rand.Text() + ">"
	t.Setenv("OPENAI_API_KEY", key)
	t.Setenv("OPENAI_BASE_URL", "http://invalid.invalid")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key || r.URL.Path != "/v1/chat/completions" {
			t.Error("incorrect configuration wiring")
		}
		var body map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid body")
		}
		if _, ok := body["model"]; ok {
			t.Error("model field exposed")
		}
		n := calls.Add(1)
		if n%2 == 1 {
			if _, ok := body["response_format"]; ok {
				t.Error("generation is not ordinary Prompt")
			}
			serveCompletion(w, "result")
		} else {
			if _, ok := body["response_format"]; !ok {
				t.Error("evaluation lacks schema")
			}
			serveCompletion(w, `{"achieved":true,"rationale":"Satisfied"}`)
		}
	}))
	defer server.Close()
	path := configFile(t, "openai:\n  environment:\n    OPENAI_BASE_URL: "+server.URL+"/v1\n")
	var out, diag bytes.Buffer
	if err := run(context.Background(), []string{"--trace", "--config", path, "request " + key}, &out, &diag); err != nil {
		t.Fatal(err)
	}
	if out.String() != "result\n" || calls.Load() != 2 {
		t.Fatal("wrong application output or call count")
	}
	for _, part := range []string{"Outgoing generation prompt", "Outgoing evaluation", "Status: achieved", "Requests: 2", "Total tokens: 14", "[redacted]"} {
		if !strings.Contains(diag.String(), part) {
			t.Errorf("missing trace/report: %s", part)
		}
	}
	if strings.Contains(diag.String(), key) || strings.Contains(diag.String(), server.URL) {
		t.Fatal("credential/configuration printed")
	}
	t.Chdir(filepath.Dir(path))
	if err := run(context.Background(), []string{"request"}, io.Discard, io.Discard); err != nil {
		t.Fatal("default config path failed")
	}
}

func TestCLIArgumentsAndMissingConfiguration(t *testing.T) {
	for _, args := range [][]string{nil, {""}, {" "}, {"one", "two"}, {"--prompt", "x"}, {"--model", "x"}, {"--config"}} {
		if err := run(context.Background(), args, io.Discard, io.Discard); err == nil || err.Error() != usage {
			t.Fatal("invalid command accepted")
		}
	}
	for _, key := range []string{"OPENAI_BASE_URL", "OPENAI_API_KEY"} {
		t.Setenv("OPENAI_BASE_URL", "http://localhost:1")
		t.Setenv("OPENAI_API_KEY", rand.Text())
		unset(t, key)
		err := run(context.Background(), []string{"--config", configFile(t, "{}"), "prompt"}, io.Discard, io.Discard)
		if !errors.Is(err, config.ErrNotFound) {
			t.Fatal("missing config did not prevent startup")
		}
	}
}

func TestGrodtProcess(t *testing.T) {
	if os.Getenv("GRODT_TEST_CHILD") != "1" {
		return
	}
	os.Args = []string{"grodt", "--config", os.Getenv("GRODT_TEST_CONFIG"), "request"}
	main()
}

func TestProcessExitCodesAndSignals(t *testing.T) {
	for _, mode := range []string{"achieved", "incomplete", "SIGINT", "SIGTERM"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				json.NewDecoder(r.Body).Decode(&body)
				if strings.HasPrefix(mode, "SIG") {
					close(entered)
					<-r.Context().Done()
					return
				}
				if _, ok := body["response_format"]; ok {
					serveCompletion(w, fmt.Sprintf(`{"achieved":%t,"rationale":"reason"}`, mode == "achieved"))
				} else {
					serveCompletion(w, "result")
				}
			}))
			defer server.Close()
			path := configFile(t, "openai:\n  environment:\n    OPENAI_BASE_URL: "+server.URL+"/v1\n")
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(executable, "-test.run=^TestGrodtProcess$")
			cmd.Env = append(os.Environ(), "GRODT_TEST_CHILD=1", "GRODT_TEST_CONFIG="+path, "OPENAI_API_KEY="+rand.Text())
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close() // Kept open: process must not read stdin.
			var diag bytes.Buffer
			cmd.Stderr = &diag
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			if strings.HasPrefix(mode, "SIG") {
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("request did not start")
				}
				signal := os.Interrupt
				if mode == "SIGTERM" {
					signal = syscall.SIGTERM
				}
				if err := cmd.Process.Signal(signal); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("process blocked or failed to stop")
			}
			want := 0
			if mode == "incomplete" {
				want = 2
			}
			if cmd.ProcessState.ExitCode() != want {
				t.Fatal("wrong process exit code")
			}
			status := mode
			if strings.HasPrefix(mode, "SIG") {
				status = "cancelled"
			}
			if !strings.Contains(diag.String(), "Status: "+status) {
				t.Fatal("wrong final status")
			}
		})
	}
}

func TestBlockingEvaluationPreventsNextCycle(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var generations atomic.Int32
	evaluations := 0
	c := testClient{
		prompt: func(context.Context, string) (openai.TextResult, error) {
			generations.Add(1)
			return openai.TextResult{Text: "result"}, nil
		},
		mutation: func(context.Context, string, json.RawMessage, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
			evaluations++
			if evaluations == 1 {
				close(entered)
				<-release
				return evaluationResult(false, "continue"), nil
			}
			return evaluationResult(true, "done"), nil
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- execute(context.Background(), defaultObjective, "request", c, io.Discard, &terminalStatus{writer: io.Discard})
	}()
	<-entered
	if generations.Load() != 1 {
		t.Error("next generation overlapped evaluation")
	}
	close(release)
	if err := <-done; err != nil || generations.Load() != 2 {
		t.Fatal("continuation failed")
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("write failure") }

func TestOutputFailureDoesNotReportAchievement(t *testing.T) {
	c := testClient{
		prompt: func(context.Context, string) (openai.TextResult, error) {
			return openai.TextResult{Text: "result"}, nil
		},
		mutation: func(context.Context, string, json.RawMessage, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
			return evaluationResult(true, "done"), nil
		},
	}
	var diag bytes.Buffer
	err := execute(context.Background(), defaultObjective, "request", c, brokenWriter{}, &terminalStatus{writer: &diag})
	if exitCode(err) != 1 || !strings.Contains(diag.String(), "Status: failed") || strings.Contains(diag.String(), "Status: achieved") {
		t.Fatal("output failure reported as success")
	}
}
