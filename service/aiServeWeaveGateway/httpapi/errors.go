package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

// openAIErrorBody is the error shape every OpenAI-compatible client already
// knows how to parse.
type openAIErrorBody struct {
	Error openAIError `json:"error"`
}

type openAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// writeOpenAIError writes an OpenAI-shaped error body. message is a fixed,
// generic string per error class — never the upstream error text, which may
// echo backend detail this Gateway has no business repeating to the caller.
func writeOpenAIError(w http.ResponseWriter, status int, errType, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(openAIErrorBody{Error: openAIError{Message: message, Type: errType, Code: code}})
}

// handleDispatchError logs the real error server-side and writes a generic,
// classified error to the client. It is the single place a *runtime.RuntimeError
// or scheduler.ErrNoCapableNode becomes an HTTP response, so every handler
// maps errors the same way.
func handleDispatchError(w http.ResponseWriter, logger *slog.Logger, err error) {
	status, errType, code, message := dispatchErrorDetails(logger, err)
	noteDispatchError(w, code, err)
	setRetryAfter(w, err)
	writeOpenAIError(w, status, errType, code, message)
}

// modelUnavailableRetryAfter is the Retry-After, in seconds, sent with
// scheduler.ErrModelUnavailable: the request already waited for the node, so
// a client that retries this much later is most likely served.
//
// modelUnavailableRetryAfter 是随 scheduler.ErrModelUnavailable 发送的 Retry-After
// 秒数：请求已经等过节点，客户端在这之后重试多半能得到服务。
const modelUnavailableRetryAfter = "5"

// backpressureRetryAfter is the Retry-After, in seconds, sent when every
// candidate was busy. A slot frees when some request finishes, which no one
// can predict, so this is the shortest hint that is not a busy-retry
// invitation, the same choice the tenant concurrency limit makes.
//
// backpressureRetryAfter 是所有候选都繁忙时发送的 Retry-After 秒数。名额在某个请求结束时
// 才腾出，无法预测，因此取不至于变成忙重试邀请的最短提示，与租户并发限额的取值相同。
const backpressureRetryAfter = "1"

// setRetryAfter sets the Retry-After header from a rate_limited error's
// RetryAfter, rounded up to whole seconds, so a client that honours the header
// waits until the quota the scheduler is cooling down on comes back.
// scheduler.ErrModelUnavailable gets modelUnavailableRetryAfter and
// backpressure gets backpressureRetryAfter. Any other error, or a
// rate_limited one with no RetryAfter, leaves the header unset.
//
// setRetryAfter 依据 rate_limited 错误的 RetryAfter 设置 Retry-After 响应头（向上取整
// 到秒），让遵守该头的客户端等到调度器正在冷却的那份额度恢复。
// scheduler.ErrModelUnavailable 设置为 modelUnavailableRetryAfter，backpressure 设置为
// backpressureRetryAfter。其他错误、或没有 RetryAfter 的 rate_limited 错误不设置该头。
func setRetryAfter(w http.ResponseWriter, err error) {
	if errors.Is(err, scheduler.ErrModelUnavailable) {
		w.Header().Set("Retry-After", modelUnavailableRetryAfter)
		return
	}
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) {
		return
	}
	if rtErr.Code == runtime.ErrorBackpressure {
		w.Header().Set("Retry-After", backpressureRetryAfter)
		return
	}
	if rtErr.Code != runtime.ErrorRateLimited || rtErr.RetryAfter <= 0 {
		return
	}
	seconds := int64((rtErr.RetryAfter + time.Second - 1) / time.Second)
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
}

// dispatchErrorDetails classifies a scheduler/runtime dispatch failure into
// the status, error type, code and message every front door reports it as,
// and logs the real error server-side. It is the single place a
// *runtime.RuntimeError or scheduler.ErrNoCapableNode is turned into a
// classification, so a second wire protocol (anthropic.go's error body) can
// reuse the same judgment handleDispatchError already makes instead of
// re-deriving it.
func dispatchErrorDetails(logger *slog.Logger, err error) (status int, errType, code, message string) {
	if errors.Is(err, scheduler.ErrModelUnavailable) {
		logger.Warn("the requested model's nodes are temporarily unavailable")
		return http.StatusServiceUnavailable, "api_error", "model_unavailable",
			"the requested model is temporarily unavailable, retry shortly"
	}
	if errors.Is(err, scheduler.ErrNoCapableNode) {
		logger.Warn("no node can serve this request")
		return http.StatusNotFound, "invalid_request_error", "model_not_found",
			"the requested model is not available on any connected node"
	}

	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) {
		logger.Error("dispatch failed with an unclassified error", slog.Any("error", err))
		return http.StatusInternalServerError, "api_error", "internal_error", "internal error"
	}

	logger.Error("dispatch failed",
		slog.String("code", string(rtErr.Code)),
		slog.String("runtime_id", rtErr.RuntimeID),
		slog.Bool("retryable", rtErr.Retryable),
		slog.Any("error", rtErr))
	if message := runtime.MessagesRejectionMessage(rtErr); message != "" {
		status := http.StatusBadRequest
		if rtErr.StatusCode == http.StatusRequestEntityTooLarge || rtErr.StatusCode == http.StatusUnprocessableEntity {
			status = rtErr.StatusCode
		}
		return status, "invalid_request_error", string(rtErr.Code), message
	}

	return classify(rtErr.Code)
}

// classify maps a runtime.ErrorCode to the HTTP status and generic body an
// OpenAI-compatible client gets back. The mapping only needs to be
// consistent, not identical to OpenAI's own — no client branches on our
// exact code string, only on the type and status.
func classify(code runtime.ErrorCode) (status int, errType, errCode, message string) {
	switch code {
	case runtime.ErrorInvalidConfig, runtime.ErrorProtocol:
		return http.StatusBadRequest, "invalid_request_error", string(code), "the request could not be processed"
	case runtime.ErrorUnauthorized:
		return http.StatusUnauthorized, "invalid_request_error", string(code), "the node rejected this request's credentials"
	case runtime.ErrorCapability:
		return http.StatusBadRequest, "invalid_request_error", string(code), "the selected model does not support this request"
	case runtime.ErrorRateLimited, runtime.ErrorBackpressure:
		return http.StatusTooManyRequests, "rate_limit_error", string(code), "no node had capacity for this request; retry shortly"
	case runtime.ErrorTimeout:
		return http.StatusGatewayTimeout, "timeout_error", string(code), "the request timed out"
	case runtime.ErrorResponseTooLarge:
		return http.StatusRequestEntityTooLarge, "invalid_request_error", string(code), "the response exceeded the size limit"
	case runtime.ErrorConnection, runtime.ErrorClosed:
		return http.StatusServiceUnavailable, "api_error", string(code), "the selected node is unavailable"
	case runtime.ErrorUpstream:
		return http.StatusBadGateway, "api_error", string(code), "the backend returned an error"
	default:
		return http.StatusInternalServerError, "api_error", string(code), "internal error"
	}
}
