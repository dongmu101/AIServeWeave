package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
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
	Cause      error
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
