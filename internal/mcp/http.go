package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
)

// wireTransport retains bounded original response JSON so schema numbers and
// explicit null/false values are not lost through the SDK's any/omitempty fields.
// The SDK alone performs negotiation, SSE delivery and JSON-RPC correlation.
type wireTransport struct {
	base    *http.Transport
	headers http.Header
	mu      sync.Mutex
	last    *capture
}
type capture struct {
	mu       sync.Mutex
	data     []byte
	sse      bool
	id       json.RawMessage
	failed   bool
	observed int
}
type captureBody struct {
	io.ReadCloser
	c *capture
}

func (b *captureBody) Read(p []byte) (int, error) {
	b.c.mu.Lock()
	if b.c.failed {
		b.c.mu.Unlock()
		return 0, errors.New("MCP response exceeds wire limit")
	}
	left := MaxWireBytes - len(b.c.data)
	b.c.mu.Unlock()
	if len(p) > left+1 {
		p = p[:left+1]
	}
	n, err := b.ReadCloser.Read(p)
	b.c.mu.Lock()
	defer b.c.mu.Unlock()
	b.c.observed = len(b.c.data) + n
	if n > left {
		b.c.failed = true
		return 0, errors.New("MCP response exceeds wire limit")
	}
	b.c.data = append(b.c.data, p[:n]...)
	return n, err
}

func (t *wireTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header = req.Header.Clone()
	// Clear retryable-request hints; native MCP POST calls are never replayed.
	r.GetBody = nil
	for k, vs := range t.headers {
		r.Header[k] = append([]string(nil), vs...)
	}
	var id json.RawMessage
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, errors.New("MCP request encoding failed")
		}
		var envelope struct {
			ID json.RawMessage `json:"id"`
		}
		err = json.NewDecoder(body).Decode(&envelope)
		body.Close()
		if err != nil {
			return nil, errors.New("MCP request encoding failed")
		}
		id = envelope.ID
	}
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	c := &capture{sse: strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"), id: id}
	resp.Body = &captureBody{ReadCloser: resp.Body, c: c}
	if len(id) > 0 {
		t.mu.Lock()
		t.last = c
		t.mu.Unlock()
	}
	return resp, nil
}

// reset prevents a failed request from being diagnosed using a previous response.
func (t *wireTransport) reset() { t.mu.Lock(); t.last = nil; t.mu.Unlock() }
func (t *wireTransport) overflow() (int, bool) {
	t.mu.Lock()
	c := t.last
	t.mu.Unlock()
	if c == nil {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.observed, c.failed
}
func (t *wireTransport) result() (json.RawMessage, error) {
	t.mu.Lock()
	c := t.last
	t.mu.Unlock()
	if c == nil {
		return nil, errors.New("missing MCP response")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed {
		return nil, errors.New("MCP response exceeds wire limit")
	}
	frames := [][]byte{c.data}
	if c.sse {
		frames = nil
		var data []byte
		// SSE framing inspection is only for a lossless copy; SDK has already
		// accepted the response. Join data lines according to the SSE format.
		for _, line := range bytes.Split(bytes.ReplaceAll(c.data, []byte("\r\n"), []byte("\n")), []byte("\n")) {
			if len(line) == 0 {
				if len(data) > 0 {
					frames = append(frames, data)
					data = nil
				}
				continue
			}
			if bytes.HasPrefix(line, []byte("data:")) {
				v := line[5:]
				v = bytes.TrimPrefix(v, []byte(" "))
				if len(data) > 0 {
					data = append(data, '\n')
				}
				data = append(data, v...)
			}
		}
	}
	for _, frame := range frames {
		var e struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(frame, &e) == nil && bytes.Equal(bytes.TrimSpace(e.ID), bytes.TrimSpace(c.id)) && len(e.Result) > 0 {
			return append(json.RawMessage(nil), e.Result...), nil
		}
	}
	return nil, errors.New("missing MCP result")
}
func safeError(ctx context.Context, code string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("mcp: " + code)
}
