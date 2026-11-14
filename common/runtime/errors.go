package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// ErrorCode classifies a RuntimeError so callers can branch on failure kind
// without depending on adapter-specific error types.
type ErrorCode string

const (
	ErrorInvalidConfig     ErrorCode = "invalid_config"
	ErrorConnection        ErrorCode = "connection_failed"
	ErrorProbeMismatch     ErrorCode = "probe_mismatch"
	ErrorUnauthorized      ErrorCode = "unauthorized"
	ErrorCapability        ErrorCode = "capability_unsupported"
	ErrorRateLimited       ErrorCode = "rate_limited"
	ErrorTimeout           ErrorCode = "timeout"
	ErrorUpstream          ErrorCode = "upstream_error"
	ErrorProtocol          ErrorCode = "protocol_error"
	ErrorResponseTooLarge  ErrorCode = "response_too_large"
	ErrorCancelUnsupported ErrorCode = "cancel_unsupported"
	ErrorClosed            ErrorCode = "runtime_closed"
	// ErrorBackpressure marks local concurrency-limit rejections. It is
	// deliberately distinct from ErrorRateLimited/ErrorUpstream: those mean
	// the backend said no, this means the Agent itself declined to send the
	// request, which the "backpressure" metrics result label depends on.
	ErrorBackpressure ErrorCode = "backpressure"
)

// Sentinel errors for conditions that Registry, Manager, the concurrency
// limiter and the ComfyUI cancel path need to distinguish with errors.Is.
// They only ever appear as a RuntimeError.Cause, never returned bare, so
// callers can match either the sentinel or the RuntimeError.Code.
var (
	ErrFactoryAlreadyRegistered = errors.New("runtime: factory already registered")
	ErrRuntimeKindUnsupported   = errors.New("runtime: runtime kind unsupported")
	ErrRuntimeIDDuplicated      = errors.New("runtime: runtime id duplicated")
	ErrRuntimeNotFound          = errors.New("runtime: runtime not found")
	ErrCancelUnsupported        = errors.New("runtime: cancel unsupported")
	ErrCapabilityUnknown        = errors.New("runtime: capability unknown")
	ErrCapabilityUnsupported    = errors.New("runtime: capability unsupported")
	ErrConcurrencyLimit         = errors.New("runtime: concurrency limit reached")
	ErrRuntimeClosed            = errors.New("runtime: runtime closed")
	// ErrNotDelivered marks a failure that certainly happened before the
	// backend received the request: a dial that never connected, or a
	// tunnel request that was not fully sent. Only such a failure lets a
	// non-idempotent operation (a workflow submit) be sent again elsewhere;
	// any other connection failure leaves its fate unknown.
	//
	// ErrNotDelivered 标记一次确定发生在后端收到请求之前的失败：从未连通的拨号，或未完整
	// 发出的隧道请求。只有这样的失败才允许把非幂等操作（工作流提交）改发到别处；其他连接
	// 失败都让结果成为未知。
	ErrNotDelivered = errors.New("runtime: request was not delivered to the backend")
)

// RuntimeError is the error type returned by all Runtime operations.
// Message must already be safe to expose to callers and logs; Cause may
// carry unsanitized detail for diagnostics and is reachable only through
// Unwrap/errors.As, never through Error() or Message.
type RuntimeError struct {
	Code       ErrorCode
	RuntimeID  string
	Kind       Kind
	Operation  string
	StatusCode int // 0 when not applicable
	Message    string
	Retryable  bool
	// RetryAfter is how long the backend asked to be left alone, set only on
	// an ErrorRateLimited whose backend reported when its quota resets. Zero
	// means unknown, not "retry immediately".
	//
	// RetryAfter 是后端要求的冷却时长，只在后端报告了额度重置时间的
	// ErrorRateLimited 上设置。零值表示未知，而不是「立即重试」。
	RetryAfter time.Duration
	// Upstream is the native upstream's own answer to a failed Messages
	// round — status, response headers and error document — when the Agent
	// captured it below the CLI. Nil for every other error.
	//
	// Upstream 是 Agent 在 CLI 之下截获到的原生上游对失败 Messages 回合的应答——状态码、
	// 响应头与错误文档。其他错误均为 nil。
	Upstream *UpstreamResponse
	Cause    error
}

func (e *RuntimeError) Error() string {
	msg := e.Message
	if msg == "" && e.Cause != nil {
		msg = e.Cause.Error()
	}
	return fmt.Sprintf("runtime[%s/%s] %s: %s: %s", e.Kind, e.RuntimeID, e.Operation, e.Code, msg)
}

// Unwrap exposes Cause to errors.Is/errors.As.
func (e *RuntimeError) Unwrap() error { return e.Cause }

// Is reports whether target is a *RuntimeError with the same Code, so
// callers can write errors.Is(err, &runtime.RuntimeError{Code: runtime.ErrorTimeout})
// without needing the RuntimeID, Operation or Cause to match.
func (e *RuntimeError) Is(target error) bool {
	t, ok := target.(*RuntimeError)
	if !ok {
		return false
	}
	return t.Code == e.Code
}

// ErrorCodeFromStatus maps an upstream HTTP status code to a RuntimeError
// code: 401/403 to unauthorized, 429 to rate limited, 5xx to upstream error,
// and any other 4xx to protocol error.
func ErrorCodeFromStatus(status int) ErrorCode {
	switch {
	case status == 401 || status == 403:
		return ErrorUnauthorized
	case status == 429:
		return ErrorRateLimited
	case status >= 500:
		return ErrorUpstream
	default:
		return ErrorProtocol
	}
}

// maxRetryAfter bounds what ParseRetryAfter accepts, so a misbehaving
// upstream cannot park a candidate for days with one header.
//
// maxRetryAfter 限定 ParseRetryAfter 接受的上限，免得一个行为异常的上游用一个响应头就把
// 候选搁置好几天。
const maxRetryAfter = 24 * time.Hour

// ParseRetryAfter reads an HTTP Retry-After header in its delay-seconds form.
// The HTTP-date form, a malformed value, zero and anything beyond 24 hours all
// return zero, which RuntimeError.RetryAfter reads as "unknown".
//
// ParseRetryAfter 读取 HTTP Retry-After 响应头的秒数形式。HTTP 日期形式、格式错误的值、
// 零以及超过 24 小时的值都返回零，RuntimeError.RetryAfter 把零读作「未知」。
func ParseRetryAfter(value string) time.Duration {
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || seconds <= 0 || seconds > int64(maxRetryAfter/time.Second) {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// ClassifyTransportError maps a transport-level error (from an HTTP round
// trip, dial or context) to a RuntimeError code, distinguishing timeouts
// from other connection failures such as DNS or TLS errors.
func ClassifyTransportError(err error) ErrorCode {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ErrorTimeout
	}
	return ErrorConnection
}

// Redact replaces every occurrence of each non-empty secret in s with
// "[REDACTED]". Adapters use it to strip API keys, Authorization headers and
// other configured secrets from upstream error bodies before they reach a
// RuntimeError's Message or any log line.
func Redact(s string, secrets ...string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		s = strings.ReplaceAll(s, secret, "[REDACTED]")
	}
	return s
}
