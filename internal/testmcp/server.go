// Package testmcp supplies deterministic, domain-neutral MCP HTTP fixtures.
package testmcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
)

type Server struct {
	Tools  json.RawMessage
	List   func(string) json.RawMessage
	Call   func(*http.Request, string, json.RawMessage) (json.RawMessage, error)
	Check  func(*http.Request)
	SSE    bool
	Calls  atomic.Int64
	Closed atomic.Int64
	Lists  atomic.Int64
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.Check != nil {
		s.Check(r)
	}
	if r.Method == http.MethodDelete {
		s.Closed.Add(1)
		w.WriteHeader(204)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		w.WriteHeader(400)
		return
	}
	if len(req.ID) == 0 {
		w.WriteHeader(202)
		return
	}
	var result json.RawMessage
	var err error
	switch req.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "fixture-session")
		result = json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"arbitrary-fixture","version":"1"}}`)
	case "tools/list":
		s.Lists.Add(1)
		var p struct {
			Cursor string `json:"cursor"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if s.List != nil {
			result = s.List(p.Cursor)
		} else {
			result = append([]byte(`{"tools":`), s.Tools...)
			result = append(result, '}')
		}
	case "tools/call":
		s.Calls.Add(1)
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if s.Call != nil {
			result, err = s.Call(r, p.Name, p.Arguments)
		} else {
			result = json.RawMessage(`{"content":[{"type":"text","text":"fixture result"}],"structuredContent":{"action":"increment"}}`)
		}
	default:
		err = fmt.Errorf("unsupported method")
	}
	envelope := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}
	if err != nil {
		delete(envelope, "result")
		envelope["error"] = map[string]any{"code": -32601, "message": "fixture protocol failure"}
	}
	b, _ := json.Marshal(envelope)
	if s.SSE && req.Method == "tools/call" {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}
}

const Tools = `[{"name":"lookup","description":"Return the fixture observation.","inputSchema":{"type":"object","properties":{"key":{"type":"string"}},"required":["key"],"additionalProperties":false}}]`
