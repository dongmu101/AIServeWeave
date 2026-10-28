// Package openai implements the shared HTTP transport used by the vLLM,
// SGLang, Ollama, hosted-provider and Anthropic runtime adapters to talk to
// an OpenAI-compatible (or, for Anthropic, Messages) backend. It owns URL
// construction, authentication headers, response size limits and error
// normalization; adapters only add their own endpoints and DTOs on top of
// Client.
//
// Package openai 实现 vLLM、SGLang、Ollama、托管供应商与 Anthropic 运行时适配器
// 共用的 HTTP 传输层，用于访问 OpenAI 兼容后端（Anthropic 则为 Messages 后端）。
// 它负责 URL 构造、认证请求头、响应大小上限与错误归一化；适配器只在 Client 之上
// 添加各自的端点与 DTO。
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"AIServeWeave/common/runtime"
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
	// VersionedBaseURL means BaseURL already ends at the API version root
	// (https://api.openai.com/v1, https://open.bigmodel.cn/api/paas/v4), as
	// hosted providers document it, so every request path drops its leading
	// "/v1" before being joined on.
	//
	// VersionedBaseURL 表示 BaseURL 已经包含 API 版本根路径（如
	// https://api.openai.com/v1、https://open.bigmodel.cn/api/paas/v4），与托管
	// 供应商文档的写法一致；拼接前每个请求路径都会去掉开头的 "/v1"。
	VersionedBaseURL bool
	// AuthHeader, when set, carries APIKey verbatim in that header (for
	// example "x-api-key") instead of "Authorization: Bearer". The key is
	// still redacted from errors either way.
	//
	// AuthHeader 非空时以该请求头原样携带 APIKey（例如 "x-api-key"），而不是
	// "Authorization: Bearer"。两种方式下密钥都会从错误中脱敏。
	AuthHeader string
	// MaxCompletionTokens sends ChatRequest.MaxTokens as
	// "max_completion_tokens", the name OpenAI's reasoning models and
	// MiniMax require, instead of "max_tokens".
	//
	// MaxCompletionTokens 把 ChatRequest.MaxTokens 以 "max_completion_tokens"
	// 发送（OpenAI 推理模型与 MiniMax 要求的字段名），而不是 "max_tokens"。
	MaxCompletionTokens bool
	// RefuseRedirects returns a redirect response as a protocol_error
	// ("redirect not followed") instead of following it. Go forwards custom headers such as x-api-key
	// across hosts on redirect, so a remote endpoint must never be able to
	// steer a credential to an address the operator did not declare.
	//
	// RefuseRedirects 把重定向响应作为 protocol_error（"redirect not followed"）
	// 返回，而不是跟随它。Go 在跨主机
	// 重定向时会转发 x-api-key 这类自定义请求头，因此绝不能让远端端点把凭据
	// 引到运维未声明的地址。
	RefuseRedirects bool
	// ReasoningParser is runtime.Config.ReasoningParser: the fallback that
	// splits inline <think> reasoning out of replies. Empty means none.
	//
	// ReasoningParser 即 runtime.Config.ReasoningParser：把内联的 <think> 思考从回复
	// 里切分出来的兜底解析器。为空表示不启用。
	ReasoningParser string
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
	versioned    bool
	authHeader   string
	maxCompl     bool

	reasoningParser string
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
	if cfg.ReasoningParser != "" && newThinkSplitter(cfg.ReasoningParser) == nil {
		return nil, fmt.Errorf("openai: unknown reasoning parser %q", cfg.ReasoningParser)
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
	if cfg.RefuseRedirects {
		refusing := *httpClient
		refusing.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		httpClient = &refusing
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
		versioned:    cfg.VersionedBaseURL,
		authHeader:   cfg.AuthHeader,
		maxCompl:     cfg.MaxCompletionTokens,

		reasoningParser: cfg.ReasoningParser,
	}, nil
}

// resolve joins path onto the client's base URL, preserving any path prefix
// on BaseURL instead of overwriting it. With VersionedBaseURL the path's
// leading "/v1" is dropped, because BaseURL already carries the version.
//
// resolve 把 path 拼接到客户端的基础 URL 上，保留 BaseURL 的路径前缀而不是覆盖
// 它。启用 VersionedBaseURL 时去掉 path 开头的 "/v1"，因为 BaseURL 已含版本段。
func (c *Client) resolve(path string) *url.URL {
	if c.versioned {
		if rest, ok := strings.CutPrefix(path, "/v1/"); ok {
			path = "/" + rest
		}
	}
	path, query, _ := strings.Cut(path, "?") // a query is the caller's, never the base URL's / 查询串只来自调用方，从不来自基础 URL
	resolved := *c.baseURL
	resolved.Path = strings.TrimSuffix(c.baseURL.Path, "/") + path
	resolved.RawQuery = query
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
	c.setAuth(req.Header)
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// setAuth attaches the API key, if any, in the configured header.
// setAuth 按配置的请求头附加 API 密钥（若有）。
func (c *Client) setAuth(h http.Header) {
	switch {
	case c.apiKey == "":
	case c.authHeader != "":
		h.Set(c.authHeader, c.apiKey)
	default:
		h.Set("Authorization", "Bearer "+c.apiKey)
	}
}

// DoStream sends operation and returns the raw 2xx response with its body
// open for the caller to read and close, for streaming formats other than
// OpenAI's chat chunks. Non-2xx responses become *runtime.RuntimeError
// exactly as in Do.
//
// DoStream 发送 operation 并返回原始 2xx 响应，响应体保持打开，由调用方读取并
// 关闭，用于 OpenAI 聊天分片之外的流式格式。非 2xx 响应与 Do 一样转换为
// *runtime.RuntimeError。
func (c *Client) DoStream(ctx context.Context, operation, method, path string, reqBody any) (*http.Response, error) {
	return c.doRaw(ctx, operation, method, path, reqBody)
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

// doMultipart streams a multipart/form-data request: fields as plain form
// values, plus one file part named fileField whose bytes come from body.
// The multipart envelope is written by a goroutine into an io.Pipe, so body
// is read and forwarded as the transport consumes it — never buffered whole,
// the same discipline doRaw's JSON path does not need because a ChatRequest
// is already a bounded in-memory value.
//
// doMultipart 流式发送一个 multipart/form-data 请求：fields 作为普通表单字段，
// 外加一个名为 fileField 的文件分片，其字节来自 body。multipart 信封由一个
// goroutine 写入 io.Pipe，因此 body 随传输层的消费而被读取并转发——绝不整体
// 缓冲，这是 doRaw 的 JSON 路径不需要的纪律，因为 ChatRequest 本就是一个有界
// 的内存内值。
func (c *Client) doMultipart(ctx context.Context, operation, path string, fields map[string]string, fileField, filename string, body io.Reader) (*http.Response, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		err := func() error {
			for name, value := range fields {
				if err := mw.WriteField(name, value); err != nil {
					return err
				}
			}
			part, err := mw.CreateFormFile(fileField, filename)
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, body); err != nil {
				return err
			}
			return mw.Close()
		}()
		_ = pw.CloseWithError(err)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.resolve(path).String(), pr)
	if err != nil {
		return nil, c.localError(operation, runtime.ErrorInvalidConfig, fmt.Sprintf("build request: %v", err))
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	c.setAuth(req.Header)
	for k, v := range c.headers {
		req.Header.Set(k, v)
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
	if status >= 300 && status < 400 {
		// A refused redirect: its body and target are not worth surfacing.
		// 被拒绝的重定向：其正文与目标地址不对外暴露。
		msg = fmt.Sprintf("redirect not followed (HTTP %d)", status)
	}
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
