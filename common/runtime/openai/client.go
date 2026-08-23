// Package openai implements the shared HTTP transport used by the vLLM,
// SGLang and Ollama runtime adapters to talk to an OpenAI-compatible
// backend. It owns URL construction, authentication headers, response size
// limits and error normalization; adapters only add their own endpoints and
// DTOs on top of Client.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"AIServeWeave/service/aiServeWeaveAgent/runtime"
)

// defaultMaxResponseBytes bounds a single response body when the caller does
// not configure MaxResponseBytes, preventing unbounded memory growth from a
// misbehaving or malicious backend.
const defaultMaxResponseBytes = 4 << 20 // 4 MiB

// hopByHopHeaders are headers a caller must not override via Config.Headers,
// either because they are connection-scoped (RFC 7230 §6.1) or because
// overriding them could corrupt the request Client builds.
var hopByHopHeaders = map[string]bool{
	"Host":                true,
	"Content-Length":      true,
	"Connection":          true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
}

// ClientConfig configures a Client. BaseURL must be an http(s) URL without
// userinfo, query or fragment; any path is treated as a prefix that is
// preserved on every request.
type ClientConfig struct {
	BaseURL          string
	APIKey           string
	Headers          map[string]string
	HTTPClient       *http.Client
	MaxResponseBytes int64
	Kind             runtime.Kind
	RuntimeID        string
}

// Client is the shared OpenAI-compatible HTTP client. It is safe for
// concurrent use.
type Client struct {
	baseURL      *url.URL
	httpClient   *http.Client
	apiKey       string
	headers      map[string]string
	maxRespBytes int64
	kind         runtime.Kind
	runtimeID    string
}

// NewClient validates cfg and builds a Client. It performs no network I/O.
func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("openai: base URL is required")
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("openai: invalid base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("openai: base URL scheme must be http or https, got %q", u.Scheme)
	}
	if u.User != nil {
		return nil, fmt.Errorf("openai: base URL must not contain userinfo")
	}
	if u.RawQuery != "" {
		return nil, fmt.Errorf("openai: base URL must not contain a query string")
	}
	if u.Fragment != "" {
		return nil, fmt.Errorf("openai: base URL must not contain a fragment")
	}
	for name := range cfg.Headers {
		if hopByHopHeaders[http.CanonicalHeaderKey(name)] {
			return nil, fmt.Errorf("openai: header %q must not be set via config", name)
		}
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	maxBytes := cfg.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxResponseBytes
	}

	return &Client{
		baseURL:      u,
		httpClient:   httpClient,
		apiKey:       cfg.APIKey,
		headers:      cfg.Headers,
		maxRespBytes: maxBytes,
		kind:         cfg.Kind,
		runtimeID:    cfg.RuntimeID,
	}, nil
}

// resolve joins path onto the client's base URL, preserving any path prefix
// on BaseURL instead of overwriting it.
func (c *Client) resolve(path string) *url.URL {
	resolved := *c.baseURL
	resolved.Path = strings.TrimSuffix(c.baseURL.Path, "/") + path
	resolved.RawQuery = ""
	return &resolved
}

func (c *Client) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	u := c.resolve(path)
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// Do sends operation as an HTTP request. If reqBody is non-nil, it is
// marshaled as the JSON request body. If out is non-nil and the response is
// a 2xx with a body, the body is decoded into out.
//
// Non-2xx responses and transport failures are returned as
// *runtime.RuntimeError with Message already redacted of the configured API
// key.
func (c *Client) Do(ctx context.Context, operation, method, path string, reqBody, out any) error {
	resp, err := c.doRaw(ctx, operation, method, path, reqBody)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBytes, truncated, err := readLimited(resp.Body, c.maxRespBytes)
	if err != nil {
		return c.transportError(ctx, operation, err)
	}
	if truncated {
		return c.tooLargeError(operation, resp.StatusCode)
	}

	if out != nil && len(respBytes) > 0 {
		if err := json.Unmarshal(respBytes, out); err != nil {
			return &runtime.RuntimeError{
				Code:       runtime.ErrorProtocol,
				RuntimeID:  c.runtimeID,
				Kind:       c.kind,
				Operation:  operation,
				StatusCode: resp.StatusCode,
				Message:    "decode response: invalid JSON",
				Cause:      err,
			}
		}
	}
	return nil
}

// doRaw sends operation as an HTTP request and returns the raw response for
// a 2xx status, with the body left open for the caller to read (and close).
// Non-2xx responses are read up to the configured size limit, converted to
// a *runtime.RuntimeError, and their body closed before returning — callers
// never see a non-2xx *http.Response.
//
// doRaw is the primitive Do and the streaming chat/workflow paths share, so
// URL building, auth, and error normalization stay in one place.
func (c *Client) doRaw(ctx context.Context, operation, method, path string, reqBody any) (*http.Response, error) {
	var bodyBytes []byte
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return nil, c.localError(operation, runtime.ErrorInvalidConfig, fmt.Sprintf("encode request: %v", err))
		}
		bodyBytes = b
	}

	req, err := c.newRequest(ctx, method, path, bodyBytes)
	if err != nil {
		return nil, c.localError(operation, runtime.ErrorInvalidConfig, fmt.Sprintf("build request: %v", err))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.transportError(ctx, operation, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		respBytes, truncated, err := readLimited(resp.Body, c.maxRespBytes)
		if err != nil {
			return nil, c.transportError(ctx, operation, err)
		}
		if truncated {
			return nil, c.tooLargeError(operation, resp.StatusCode)
		}
		return nil, c.errorFromResponse(operation, resp.StatusCode, respBytes)
	}
	return resp, nil
}

func (c *Client) tooLargeError(operation string, statusCode int) *runtime.RuntimeError {
	return &runtime.RuntimeError{
		Code:       runtime.ErrorResponseTooLarge,
		RuntimeID:  c.runtimeID,
		Kind:       c.kind,
		Operation:  operation,
		StatusCode: statusCode,
		Message:    "response exceeded maximum allowed size",
	}
}

func (c *Client) localError(operation string, code runtime.ErrorCode, message string) *runtime.RuntimeError {
	return &runtime.RuntimeError{
		Code:      code,
		RuntimeID: c.runtimeID,
		Kind:      c.kind,
		Operation: operation,
		Message:   message,
	}
}

func (c *Client) transportError(ctx context.Context, operation string, err error) *runtime.RuntimeError {
	var code runtime.ErrorCode
	switch {
	case ctx.Err() != nil:
		// The context ended (deadline or explicit cancel); report it as a
		// timeout regardless of how the transport surfaced the failure.
		code = runtime.ErrorTimeout
	default:
		code = runtime.ClassifyTransportError(err)
		if code == "" {
			code = runtime.ErrorConnection
		}
	}
	return &runtime.RuntimeError{
		Code:      code,
		RuntimeID: c.runtimeID,
		Kind:      c.kind,
		Operation: operation,
		Message:   runtime.Redact(err.Error(), c.apiKey),
		Cause:     err,
		Retryable: code == runtime.ErrorConnection || code == runtime.ErrorTimeout,
	}
}

func (c *Client) errorFromResponse(operation string, status int, body []byte) *runtime.RuntimeError {
	code := runtime.ErrorCodeFromStatus(status)
	msg := runtime.Redact(parseErrorMessage(body), c.apiKey)
	return &runtime.RuntimeError{
		Code:       code,
		RuntimeID:  c.runtimeID,
		Kind:       c.kind,
		Operation:  operation,
		StatusCode: status,
		Message:    msg,
		Retryable:  code == runtime.ErrorRateLimited || code == runtime.ErrorUpstream,
	}
}

// readLimited reads up to limit+1 bytes from r. If more than limit bytes
// were available, it reports truncated=true instead of reading the rest of
// r, so a huge response body cannot be buffered in memory.
func readLimited(r io.Reader, limit int64) (data []byte, truncated bool, err error) {
	data, err = io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}
