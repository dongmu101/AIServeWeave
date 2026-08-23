package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

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
	if errors.Is(err, scheduler.ErrNoCapableNode) {
		logger.Warn("no node can serve this request")
		writeOpenAIError(w, http.StatusNotFound, "invalid_request_error", "model_not_found",
			"the requested model is not available on any connected node")
		return
	}

	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) {
		logger.Error("dispatch failed with an unclassified error", slog.Any("error", err))
		writeOpenAIError(w, http.StatusInternalServerError, "api_error", "internal_error", "internal error")
		return
	}

	logger.Error("dispatch failed",
		slog.String("code", string(rtErr.Code)),
		slog.String("runtime_id", rtErr.RuntimeID),
		slog.Bool("retryable", rtErr.Retryable),
		slog.Any("error", rtErr))

	status, errType, code, message := classify(rtErr.Code)
	writeOpenAIError(w, status, errType, code, message)
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
