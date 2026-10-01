package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const MaxHTTPRedirects = 3
const HTTPTimeout = 10 * time.Second
const MaxHTTPBodyBytes = 1 << 20
const MaxHTTPHeaderBytes = 64 << 10

type HTTPRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

// Body is encoded as base64 by encoding/json, preserving arbitrary response bytes.
type HTTPResult struct {
	StatusCode int         `json:"status_code,omitempty"`
	Headers    http.Header `json:"headers,omitempty"`
	Body       []byte      `json:"body,omitempty"`
	Error      string      `json:"error,omitempty"`
}

var errRedirectLimit = errors.New("redirect limit")
var errHTTPPolicy = errors.New("HTTP capability denied")

// checkHTTPURL is shared by the original destination and every redirect. Future
// host capability policy belongs here so redirects cannot bypass that policy.
func (s *Store) checkHTTPURL(u *url.URL) error {
	if !s.allowHTTP || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return errHTTPPolicy
	}
	return nil
}
func (s *Store) executeHTTP(ctx context.Context, payload json.RawMessage) HTTPResult {
	if !s.allowHTTP {
		return HTTPResult{Error: "http_disabled"}
	}
	var in HTTPRequest
	d := json.NewDecoder(bytes.NewReader(payload))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || strings.TrimSpace(in.Method) == "" {
		return HTTPResult{Error: "invalid_http_request"}
	}
	ctx, cancel := context.WithTimeout(ctx, HTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, in.Method, in.URL, bytes.NewReader(in.Body))
	if err != nil {
		return HTTPResult{Error: "invalid_http_request"}
	}
	if s.checkHTTPURL(req.URL) != nil {
		return HTTPResult{Error: "http_policy_denied"}
	}
	headerBytes := 0
	for k, v := range in.Headers {
		headerBytes += len(k) + len(v)
		req.Header.Set(k, v)
	}
	if headerBytes > MaxHTTPHeaderBytes {
		return HTTPResult{Error: "header_limit"}
	}
	if len(in.Body) > 0 && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	transport := &http.Transport{DisableKeepAlives: true, MaxResponseHeaderBytes: MaxHTTPHeaderBytes}
	// Separate from the LLM client: no credentials, cookies, proxy configuration,
	// or connection reuse that could introduce transparent request retries.
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if err := s.checkHTTPURL(next.URL); err != nil {
			return err
		}
		if len(via) > MaxHTTPRedirects {
			return errRedirectLimit
		}
		return nil
	}}
	response, err := client.Do(req)
	if err != nil {
		code := "http_failed"
		if errors.Is(err, errRedirectLimit) {
			code = "redirect_limit"
		} else if errors.Is(err, errHTTPPolicy) {
			code = "http_policy_denied"
		} else if errors.Is(err, context.DeadlineExceeded) {
			code = "http_timeout"
		} else if errors.Is(err, context.Canceled) {
			code = "http_cancelled"
		}
		return HTTPResult{Error: code}
	}
	defer response.Body.Close()
	headerBytes = 0
	for k, values := range response.Header {
		for _, v := range values {
			headerBytes += len(k) + len(v)
		}
	}
	if headerBytes > MaxHTTPHeaderBytes {
		return HTTPResult{Error: "header_limit"}
	}
	result := HTTPResult{StatusCode: response.StatusCode, Headers: response.Header}
	result.Body, err = io.ReadAll(io.LimitReader(response.Body, MaxHTTPBodyBytes+1))
	if len(result.Body) > MaxHTTPBodyBytes {
		result.Body = nil
		result.Error = "body_limit"
	} else if err != nil {
		result.Body = nil
		result.Error = "body_read_failed"
	}
	return result
}
