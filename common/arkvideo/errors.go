package arkvideo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/volcengine/ark-runtime-go/arkruntime/model"
)

// Kind classifies a failed call without quoting the server.
//
// Kind 对失败的调用分类，不引用服务端原文。
type Kind string

const (
	// KindAuth: Ark rejected the key (401/403).
	// KindAuth：方舟拒绝了 Key（401/403）。
	KindAuth Kind = "auth"
	// KindInvalid: Ark rejected the request (400/422), or it was malformed
	// before it was sent.
	// KindInvalid：方舟拒绝了请求（400/422），或请求在发出前就不合法。
	KindInvalid Kind = "invalid_request"
	// KindContentRejected: the input failed Ark's content review.
	// KindContentRejected：输入未通过方舟的内容审核。
	KindContentRejected Kind = "content_rejected"
	// KindRateLimited: Ark refused for rate or quota (429).
	// KindRateLimited：方舟因限流或额度拒绝（429）。
	KindRateLimited Kind = "rate_limited"
	// KindNotFound: no such task, or the result URL is gone (404).
	// KindNotFound：任务不存在，或结果地址已失效（404）。
	KindNotFound Kind = "not_found"
	// KindConflict: the task is in a state that does not allow this (409).
	// KindConflict：任务所处的状态不允许该操作（409）。
	KindConflict Kind = "conflict"
	// KindNetwork: no connection, DNS failure, timeout or a refused address.
	// KindNetwork：连不上、DNS 失败、超时或地址被拒绝。
	KindNetwork Kind = "network"
	// KindUpstream: any other failure on Ark's side.
	// KindUpstream：方舟一侧的其他失败。
	KindUpstream Kind = "upstream"
)

// Error is a classified failure. Code is Ark's error code when it is a plain
// identifier; Ark's message is dropped.
//
// Error 是一个已分类的失败。Code 在它是一个简单标识符时为方舟的错误码；方舟的消息被丢弃。
type Error struct {
	Kind   Kind
	Status int
	Code   string
}

func (e *Error) Error() string {
	msg := "arkvideo: " + string(e.Kind)
	if e.Status != 0 {
		msg += fmt.Sprintf(" (status %d)", e.Status)
	}
	if e.Code != "" {
		msg += " " + e.Code
	}
	return msg
}

// classify turns an SDK error into an *Error. The context's own error is
// returned unchanged, so a caller can tell a stop from a failure.
//
// classify 把 SDK 的错误转成 *Error。上下文自身的错误原样返回，调用方因此能区分停止与失败。
func classify(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var apiErr *model.APIError
	if errors.As(err, &apiErr) {
		return statusError(apiErr.HTTPStatusCode, safeCode(apiErr.Code))
	}
	var reqErr *model.RequestError
	if errors.As(err, &reqErr) {
		if isNetwork(reqErr.Err) {
			return &Error{Kind: KindNetwork}
		}
		// The SDK reports a transport failure as a RequestError with status
		// 500 and a non-network cause only for malformed requests or bodies
		// it could not decode.
		// SDK 只把格式错误的请求或无法解码的响应体报告为状态 500、原因不是网络错误的
		// RequestError。
		if reqErr.HTTPStatusCode >= 200 && reqErr.HTTPStatusCode < 300 {
			return &Error{Kind: KindUpstream, Status: reqErr.HTTPStatusCode}
		}
		if reqErr.HTTPStatusCode == http.StatusBadRequest {
			return &Error{Kind: KindInvalid}
		}
		return statusError(reqErr.HTTPStatusCode, "")
	}
	if isNetwork(err) {
		return &Error{Kind: KindNetwork}
	}
	return &Error{Kind: KindUpstream}
}

func statusError(status int, code string) *Error {
	e := &Error{Status: status, Code: code}
	switch {
	case ContentRejected(code):
		e.Kind = KindContentRejected
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		e.Kind = KindAuth
	case status == http.StatusNotFound:
		e.Kind = KindNotFound
	case status == http.StatusConflict:
		e.Kind = KindConflict
	case status == http.StatusTooManyRequests:
		e.Kind = KindRateLimited
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		e.Kind = KindInvalid
	default:
		e.Kind = KindUpstream
	}
	return e
}

func isNetwork(err error) bool {
	var netErr net.Error
	var opErr *net.OpError
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) || errors.As(err, &opErr) || errors.As(err, &netErr)
}

// maxCodeBytes bounds an upstream error code kept on an Error or Task.
//
// maxCodeBytes 限制 Error 或 Task 上保留的上游错误码长度。
const maxCodeBytes = 64

// safeCode keeps code only when it is a short identifier, so nothing that
// could quote the request reaches a log or a stored record.
//
// safeCode 只在 code 是一个短标识符时保留它，使任何可能引用请求内容的文本都不会进入日志或
// 存储的记录。
func safeCode(code string) string {
	if code == "" || len(code) > maxCodeBytes {
		return ""
	}
	for _, r := range code {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return ""
		}
	}
	return code
}
