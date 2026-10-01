package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func requestResult(status, id, target string, replacement json.RawMessage) json.RawMessage {
	return encode(moduleResult{Status: status, Replace: replacement, Requests: []HostRequest{{ID: id, Kind: "http", Payload: encode(HTTPRequest{Method: "GET", URL: target})}}})
}
func hostStore(t *testing.T, enabled bool, modules ...*processor) *Store {
	t.Helper()
	defs := []Definition{}
	for i, m := range modules {
		defs = append(defs, Definition{Name: fmt.Sprintf("p%d", i), Schema: json.RawMessage(`{"type":"integer"}`), Initial: json.RawMessage(`0`), Module: m})
	}
	s, err := New(context.Background(), defs, Options{RunID: "host-test", AllowHTTP: enabled})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	if _, err = s.Push(context.Background(), "root", "input"); err != nil {
		t.Fatal(err)
	}
	return s
}
func TestHostBroadcastParentagePaginationAndCorrelation(t *testing.T) {
	var broadcastDone atomic.Bool
	var paths []string
	var s *Store
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !broadcastDone.Load() {
			t.Error("HTTP before complete broadcast")
		}
		paths = append(paths, r.URL.Path)
		w.Header().Set("X-Page", r.URL.Path)
		_, _ = w.Write([]byte{0, 255, 42})
	}))
	defer server.Close()
	var events []Event
	primary := &processor{fn: func(_ json.RawMessage, e Event) (json.RawMessage, error) {
		events = append(events, e)
		if e.Source.Kind != "http" {
			return encode(moduleResult{Status: "ignored", Requests: []HostRequest{
				{ID: "page1", Kind: "http", Payload: encode(HTTPRequest{Method: "GET", URL: server.URL + "/1"})},
				{ID: "sibling", Kind: "http", Payload: encode(HTTPRequest{Method: "GET", URL: server.URL + "/sibling"})},
			}}), nil
		}
		active, _ := s.Active()
		if active.ID != e.TaskID || active.ID != e.Source.ID || e.Correlation.Partition != "p0" {
			t.Error("incorrect active task/correlation")
		}
		var result HTTPResult
		if err := json.Unmarshal(e.Payload, &result); err != nil {
			t.Fatal(err)
		}
		if result.StatusCode != 200 || !reflect.DeepEqual(result.Body, []byte{0, 255, 42}) {
			t.Error("binary response lost")
		}
		if e.Correlation.RequestID == "page1" {
			broadcastDone.Store(false)
			return requestResult("ignored", "page2", server.URL+"/2", nil), nil
		}
		return json.RawMessage(`{"status":"mutation","replace":7}`), nil
	}}
	irrelevant := &processor{fn: func(_ json.RawMessage, e Event) (json.RawMessage, error) {
		broadcastDone.Store(true)
		return json.RawMessage(`{"status":"ignored"}`), nil
	}}
	s = hostStore(t, true, primary, irrelevant)
	emit(t, s)
	if !reflect.DeepEqual(paths, []string{"/1", "/2", "/sibling"}) {
		t.Fatalf("execution order: %v", paths)
	}
	snap := s.Snapshot()
	root := snap.Tasks.Records["host-test/task/1"]
	if root.Status != "running" || len(snap.Tasks.Stack) != 1 || snap.Intrinsic.HostExecutions != 3 || snap.Intrinsic.GlobalCycle != 0 {
		t.Fatalf("runtime: %+v", snap.Intrinsic)
	}
	work := map[string]Task{}
	for _, task := range snap.Tasks.Records {
		if task.Work != nil {
			work[task.Work.Request.ID] = task
			if task.Status != "completed" || task.Result == "" {
				t.Error("unfinished work")
			}
		}
	}
	if work["page1"].ParentID != root.ID || work["sibling"].ParentID != root.ID || work["page2"].ParentID != work["page1"].ID {
		t.Fatal("incorrect tree")
	}
	if len(events) != 4 || string(snap.Knowledge["p0"].Value) != "7" || snap.Knowledge["p1"].Metadata.LastProcessedSequence != 0 {
		t.Fatal("normal event processing failed")
	}
	// Same IDs from another causal event remain idempotent across the whole run.
	emit(t, s)
	if len(paths) != 3 || s.Snapshot().Intrinsic.HostExecutions != 3 {
		t.Fatal("duplicate executed")
	}
}

func TestHostAdmissionAndFailureIsolation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	defer server.Close()
	for _, tc := range []struct {
		name, status, replacement string
		enabled                   bool
		wantValue                 string
		wantStale                 bool
		wantCalls                 int32
	}{
		{"invalid mutation", "mutation", `"wrong"`, true, "0", true, 0},
		{"error cannot request", "error", "", true, "0", true, 0},
		{"committed despite HTTP failure", "mutation", "4", true, "4", false, 1},
		{"disabled request", "ignored", "", false, "0", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls.Load()
			var result HTTPResult
			m := &processor{fn: func(_ json.RawMessage, e Event) (json.RawMessage, error) {
				if e.Source.Kind == "http" {
					json.Unmarshal(e.Payload, &result)
					return json.RawMessage(`{"status":"ignored"}`), nil
				}
				return requestResult(tc.status, "request", server.URL, json.RawMessage(tc.replacement)), nil
			}}
			s := hostStore(t, tc.enabled, m)
			emit(t, s)
			snap := s.Snapshot()
			p := snap.Knowledge["p0"]
			if string(p.Value) != tc.wantValue || p.Metadata.Stale != tc.wantStale || calls.Load()-before != tc.wantCalls {
				t.Fatalf("partition/calls: %+v / %d", p, calls.Load()-before)
			}
			if !tc.enabled && result.Error != "http_disabled" {
				t.Fatalf("disabled: %+v", result)
			}
			if tc.wantCalls == 1 && result.StatusCode != 503 {
				t.Fatal("non-2xx lost")
			}
			if tc.wantCalls == 1 && snap.Tasks.Records["host-test/task/2"].Status != "failed" {
				t.Fatal("child failure missing")
			}
			if active, _ := s.Active(); active.ID != "host-test/task/1" {
				t.Fatal("parent not resumed")
			}
		})
	}
}

func TestRequestOnlyIgnoredRetainsStalenessAndConflictingID(t *testing.T) {
	mode := 0
	m := &processor{fn: func(_ json.RawMessage, e Event) (json.RawMessage, error) {
		if e.Source.Kind == "http" {
			return json.RawMessage(`{"status":"ignored"}`), nil
		}
		if mode == 0 {
			return json.RawMessage(`{"status":"error"}`), nil
		}
		target := "http://example.invalid/1"
		if mode == 2 {
			target = "http://example.invalid/2"
		}
		return requestResult("ignored", "same", target, nil), nil
	}}
	s := hostStore(t, false, m)
	emit(t, s)
	mode = 1
	emit(t, s)
	p := s.Snapshot().Knowledge["p0"]
	if !p.Metadata.Stale || p.Metadata.ConsecutiveFailures != 1 || p.Metadata.LastProcessedSequence != 0 {
		t.Fatal("request-only ignored asserted recovery")
	}
	mode = 2
	emit(t, s)
	if s.Snapshot().Intrinsic.HostExecutions != 1 || s.Snapshot().Knowledge["p0"].Metadata.LastError.Code != "invalid_host_request" {
		t.Fatal("conflicting reuse accepted")
	}
}

func TestHostExecutionBudgetIsTerminal(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	n := 0
	m := &processor{fn: func(_ json.RawMessage, e Event) (json.RawMessage, error) {
		n++
		return requestResult("mutation", fmt.Sprintf("r%d", n), server.URL, json.RawMessage(fmt.Sprint(n))), nil
	}}
	s := hostStore(t, true, m)
	_, err := s.Admit(context.Background(), Source{Kind: "runtime", ID: "start"}, json.RawMessage(`{}`))
	if !errors.Is(err, ErrHostBudget) || calls.Load() != MaxHostExecutions {
		t.Fatalf("budget: %v calls=%d", err, calls.Load())
	}
	snap := s.Snapshot()
	if snap.Intrinsic.GlobalCycle != 0 || snap.Intrinsic.HostExecutions != MaxHostExecutions || len(snap.Tasks.Stack) != 0 || string(snap.Knowledge["p0"].Value) != "33" {
		t.Fatal("budget lost state or changed cycles")
	}
	for _, task := range snap.Tasks.Records {
		if task.Status == "pending" || task.Status == "running" || task.Status == "waiting" {
			t.Fatal("unfinished task after exhaustion")
		}
	}
	_, err = s.Admit(context.Background(), Source{Kind: "runtime", ID: "again"}, json.RawMessage(`{}`))
	if !errors.Is(err, ErrHostBudget) || calls.Load() != MaxHostExecutions {
		t.Fatal("budget reset")
	}
}

func TestHTTPRedirectsAndBounds(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/three":
			http.Redirect(w, r, "/two", 302)
		case "/two":
			http.Redirect(w, r, "/one", 302)
		case "/one":
			http.Redirect(w, r, "/ok", 302)
		case "/four":
			http.Redirect(w, r, "/three", 302)
		case "/policy":
			http.Redirect(w, r, "http://user:password@"+r.Host+"/ok", 302)
		case "/big":
			_, _ = io.Copy(w, strings.NewReader(strings.Repeat("x", MaxHTTPBodyBytes+1)))
		case "/headers":
			w.Header().Set("X-Large", strings.Repeat("x", MaxHTTPHeaderBytes+1))
		default:
			_, _ = w.Write([]byte("ok"))
		}
	}))
	defer server.Close()
	for _, tc := range []struct {
		path, code string
		calls      int32
	}{{"/three", "", 4}, {"/four", "redirect_limit", 4}, {"/policy", "http_policy_denied", 1}, {"/big", "body_limit", 1}, {"/headers", "http_failed", 1}} {
		t.Run(tc.path, func(t *testing.T) {
			before := calls.Load()
			var result HTTPResult
			m := &processor{fn: func(_ json.RawMessage, e Event) (json.RawMessage, error) {
				if e.Source.Kind == "http" {
					json.Unmarshal(e.Payload, &result)
					return json.RawMessage(`{"status":"ignored"}`), nil
				}
				return requestResult("ignored", "r", server.URL+tc.path, nil), nil
			}}
			s := hostStore(t, true, m)
			emit(t, s)
			if result.Error != tc.code || calls.Load()-before != tc.calls || s.Snapshot().Intrinsic.HostExecutions != 1 {
				t.Fatalf("result=%+v calls=%d", result, calls.Load()-before)
			}
		})
	}
}

func TestHTTPCancellationAndJSONRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || string(b) != `{"page":1}` || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Test") != "yes" {
			t.Error("request not preserved")
		}
		cancel()
		<-r.Context().Done()
	}))
	defer server.Close()
	m := &processor{fn: func(_ json.RawMessage, e Event) (json.RawMessage, error) {
		return encode(moduleResult{Status: "ignored", Requests: []HostRequest{{ID: "cancel", Kind: "http", Payload: encode(HTTPRequest{Method: "POST", URL: server.URL, Headers: map[string]string{"X-Test": "yes"}, Body: json.RawMessage(`{"page":1}`)})}}}), nil
	}}
	s := hostStore(t, true, m)
	_, err := s.Admit(ctx, Source{Kind: "runtime", ID: "start"}, json.RawMessage(`{}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if s.Snapshot().Intrinsic.EventSequence != 1 || s.Snapshot().Tasks.Records["host-test/task/2"].Result == "" {
		t.Fatal("cancelled request lost once-only record")
	}
	if err = s.End(context.Background(), "cancelled"); err != nil {
		t.Fatal(err)
	}
}

func TestHostRequestResultLimits(t *testing.T) {
	requests := make([]HostRequest, MaxRequestsPerResult+1)
	for i := range requests {
		requests[i] = HostRequest{ID: fmt.Sprint("r", i), Kind: "http", Payload: json.RawMessage(`{}`)}
	}
	if _, err := decodeResult(encode(moduleResult{Status: "ignored", Requests: requests})); err == nil {
		t.Fatal("request cap absent")
	}
	for _, raw := range []string{`{"status":"ignored","requests":null}`, `{"status":"ignored","requests":[{"id":"x","kind":"mcp","payload":{}}]}`, `{"status":"ignored","requests":[{"id":"x","kind":"http","payload":{},"extra":1}]}`} {
		if _, err := decodeResult([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestHTTPResponseMutationValidationAndPartitionNamespacing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`"invalid integer"`)) }))
	defer server.Close()
	modules := []*processor{}
	for range 2 {
		modules = append(modules, &processor{fn: func(_ json.RawMessage, e Event) (json.RawMessage, error) {
			if e.Source.Kind != "http" {
				return requestResult("ignored", "same", server.URL, nil), nil
			}
			var response HTTPResult
			if err := json.Unmarshal(e.Payload, &response); err != nil {
				t.Fatal(err)
			}
			return encode(moduleResult{Status: "mutation", Replace: response.Body}), nil
		}})
	}
	s := hostStore(t, true, modules...)
	emit(t, s)
	snap := s.Snapshot()
	if snap.Intrinsic.HostExecutions != 2 || len(snap.Tasks.Records) != 3 {
		t.Fatal("request identities not scoped by partition")
	}
	for _, p := range snap.Knowledge {
		if string(p.Value) != "0" || !p.Metadata.Stale || p.Metadata.Version != 0 || p.Metadata.LastError.Kind != "mutation_rejected" {
			t.Fatalf("invalid HTTP-derived mutation committed: %+v", p)
		}
	}
}

func TestRequestsFromMultipleModulesRemainSiblings(t *testing.T) {
	modules := []*processor{}
	for i := range 2 {
		id := fmt.Sprintf("r%d", i)
		modules = append(modules, &processor{fn: func(_ json.RawMessage, e Event) (json.RawMessage, error) {
			if e.Source.Kind == "http" {
				return json.RawMessage(`{"status":"ignored"}`), nil
			}
			return requestResult("ignored", id, "http://example.invalid", nil), nil
		}})
	}
	s := hostStore(t, false, modules...)
	emit(t, s)
	snap := s.Snapshot()
	for _, task := range snap.Tasks.Records {
		if task.Work != nil && task.ParentID != "host-test/task/1" {
			t.Fatal("broadcast order changed causal parent")
		}
	}
}
