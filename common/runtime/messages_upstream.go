package runtime

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
)

// Upstream response passthrough bounds, shared by the Agent capture, the
// tunnel and the Gateway so every hop keeps the same headers and documents.
//
// 上游响应透传的边界，由 Agent 截获、隧道与 Gateway 共用，使每一跳保留相同的响应头与文档。
const (
	// MaxUpstreamHeaders bounds the distinct header names kept from one response.
	// MaxUpstreamHeaders 限制一次响应保留的不同响应头名称数量。
	MaxUpstreamHeaders = 128
	// MaxUpstreamHeaderValues bounds the values kept for one header name.
	// MaxUpstreamHeaderValues 限制同一响应头名称保留的值数量。
	MaxUpstreamHeaderValues = 16
	// MaxUpstreamHeaderBytes bounds the names and values kept from one response.
	// MaxUpstreamHeaderBytes 限制一次响应保留的名称与值总字节数。
	MaxUpstreamHeaderBytes = 32 << 10
	// MaxUpstreamErrorBytes bounds one upstream error document.
	// MaxUpstreamErrorBytes 限制一份上游错误文档的大小。
	MaxUpstreamErrorBytes = 64 << 10
)

// UpstreamResponse is what the native upstream answered for a Messages
// round, captured below the CLI: the final attempt's status, its response
// headers and, for a failure, the upstream's own error document. Header has
// passed SanitizeUpstreamHeader; Body has passed ValidUpstreamErrorBody.
//
// UpstreamResponse 是原生上游对一次 Messages 回合的应答，在 CLI 之下截获：最终一次尝试的
// 状态码、响应头，以及失败时上游自己的错误文档。Header 已经过 SanitizeUpstreamHeader，
// Body 已经过 ValidUpstreamErrorBody。
type UpstreamResponse struct {
	StatusCode int
	Header     http.Header
	Body       json.RawMessage
}

// UpstreamOf returns the upstream response a RuntimeError carries, or nil.
// UpstreamOf 返回 RuntimeError 携带的上游响应，没有时返回 nil。
func UpstreamOf(err error) *UpstreamResponse {
	var failure *RuntimeError
	if errors.As(err, &failure) {
		return failure.Upstream
	}
	return nil
}

// upstreamHeaderExcluded lists headers that describe the upstream connection
// or origin rather than the inference: hop-by-hop framing, body encoding the
// Gateway re-renders, cookies, and origin policies (Alt-Svc, HSTS, CORS) that
// would bind the Gateway's own host to the upstream's.
//
// upstreamHeaderExcluded 列出描述上游连接或源站而不是推理结果的响应头：逐跳帧、由 Gateway
// 重新渲染的正文编码、Cookie，以及会把 Gateway 自身域名绑定到上游策略的源站策略
// （Alt-Svc、HSTS、CORS）。
var upstreamHeaderExcluded = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true, "Proxy-Authorization": true, "Proxy-Connection": true,
	"Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
	"Content-Length": true, "Content-Encoding": true, "Content-Type": true, "Content-Range": true,
	"Set-Cookie": true, "Set-Cookie2": true, "Alt-Svc": true, "Strict-Transport-Security": true,
	"Date": true, "Server": true, "Via": true, "Vary": true, "Cache-Control": true, "Expires": true, "Etag": true,
	"Last-Modified": true, "Age": true, "Report-To": true, "Nel": true, "Content-Security-Policy": true,
	"Www-Authenticate": true,
}

func upstreamHeaderName(name string) bool {
	if name == "" || len(name) > 256 {
		return false
	}
	for _, c := range []byte(name) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0) {
			return false
		}
	}
	return true
}

func upstreamHeaderValue(value string) bool {
	for _, c := range []byte(value) {
		if c < 0x20 && c != '\t' || c == 0x7f {
			return false
		}
	}
	return true
}

// SanitizeUpstreamHeader keeps every inference header of an upstream
// response — rate limits, retry hints, request IDs, organisation and vendor
// extensions — dropping only upstreamHeaderExcluded, names the Connection
// header marks hop-by-hop, malformed names or values (so no CR/LF reaches a
// client) and whatever exceeds the bounds. It returns nil when nothing is kept.
//
// SanitizeUpstreamHeader 保留上游响应中的所有推理响应头——限额、重试提示、请求 ID、组织
// 与厂商扩展——只去掉 upstreamHeaderExcluded、被 Connection 标为逐跳的名称、格式非法的
// 名称或值（因此不会有 CR/LF 到达客户端），以及超出边界的部分。没有保留任何内容时返回 nil。
func SanitizeUpstreamHeader(h http.Header) http.Header {
	hop := make(map[string]bool)
	for _, value := range h.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			hop[http.CanonicalHeaderKey(strings.TrimSpace(name))] = true
		}
	}
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	// Deterministic order keeps the bounds independent of map iteration.
	// 固定顺序使边界裁剪不依赖 map 的遍历顺序。
	slices.Sort(names)
	out := make(http.Header)
	total := 0
	for _, name := range names {
		canonical := http.CanonicalHeaderKey(name)
		if upstreamHeaderExcluded[canonical] || hop[canonical] || strings.HasPrefix(canonical, "Access-Control-") || !upstreamHeaderName(name) {
			continue
		}
		if len(out) >= MaxUpstreamHeaders {
			break
		}
		for _, value := range h[name] {
			if len(out[canonical]) >= MaxUpstreamHeaderValues || !upstreamHeaderValue(value) || total+len(canonical)+len(value) > MaxUpstreamHeaderBytes {
				continue
			}
			total += len(canonical) + len(value)
			out[canonical] = append(out[canonical], value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ValidUpstreamErrorBody reports whether body is a bounded Anthropic error
// document: a JSON object of type "error" whose error has a string type.
// Anything else is not passed through, so a client never receives a body it
// cannot parse as the upstream's own error.
//
// ValidUpstreamErrorBody 判断 body 是否为有界的 Anthropic 错误文档：type 为 "error" 且
// error.type 为字符串的 JSON 对象。其他内容不透传，客户端因此不会收到无法按上游错误解析的正文。
func ValidUpstreamErrorBody(body []byte) bool {
	if len(body) == 0 || len(body) > MaxUpstreamErrorBytes {
		return false
	}
	var document struct {
		Type  string `json:"type"`
		Error *struct {
			Type    *string         `json:"type"`
			Message json.RawMessage `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &document) != nil || document.Type != "error" || document.Error == nil || document.Error.Type == nil || *document.Error.Type == "" {
		return false
	}
	return true
}

// UpstreamErrorDetail returns the error type and message of a valid upstream
// error document, for public protocols that render errors in their own shape.
//
// UpstreamErrorDetail 返回有效上游错误文档中的错误类型与消息，供以自身格式渲染错误的公开协议使用。
func UpstreamErrorDetail(body []byte) (errType, message string) {
	var document struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &document)
	return document.Error.Type, document.Error.Message
}

// UpstreamErrorStatus maps an error type the upstream reported inside a
// successful stream to the HTTP status the same error carries as a response.
//
// UpstreamErrorStatus 把上游在成功的流中报告的错误类型映射为同一错误作为响应时的 HTTP 状态码。
func UpstreamErrorStatus(errType string) int {
	switch errType {
	case "invalid_request_error":
		return 400
	case "authentication_error":
		return 401
	case "billing_error":
		return 402
	case "permission_error":
		return 403
	case "not_found_error":
		return 404
	case "request_too_large":
		return 413
	case "rate_limit_error":
		return 429
	case "timeout_error":
		return 504
	case "overloaded_error":
		return 529
	default:
		return 500
	}
}
