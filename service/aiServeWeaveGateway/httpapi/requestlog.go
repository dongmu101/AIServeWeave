// requestlog.go is the Gateway's side of STATUS.md's P09/C28: it classifies
// a finished, authenticated front-door request into the closed vocabulary
// the design doc requires, and defines the record shape the background
// pusher (requestlogpush.go) batches to the control plane. Nothing in this
// file reads a request body, a response body, or any free-text error — the
// only inputs are the request path and the final HTTP status code, both of
// which are already public in access logs and carry nothing that needs
// redaction.
//
// requestlog.go 是 Gateway 一侧对 STATUS.md P09/C28 的实现：把一次已完成、
// 已鉴权的前门请求，分类进设计文档要求的封闭词汇表，并定义后台推送器
// (requestlogpush.go)批量发往控制面所用的记录形状。本文件从不读取请求体、
// 响应体或任何自由文本错误——唯一的输入是请求路径与最终 HTTP 状态码，两者
// 本就出现在访问日志里，不携带任何需要脱敏的内容。
package httpapi

import (
	"time"
)

// Outcome values. Closed by design: STATUS.md's P09/C28 requires that no
// free-text error ever reaches the searchable request_logs table, so every
// possible status code must land in one of these, never in the status
// code's own message text.
//
// Outcome 取值。设计上是封闭的：STATUS.md 的 P09/C28 要求任何自由文本错误都
// 不得进入可检索的 request_logs 表，因此每一个可能的状态码都必须落进这些
// 取值之一，而绝不是状态码自身的消息文本。
const (
	OutcomeOK                  = "ok"
	OutcomeInvalidRequest      = "invalid_request"
	OutcomeUnauthorized        = "unauthorized"
	OutcomeForbidden           = "forbidden"
	OutcomeNotFound            = "not_found"
	OutcomeRateLimited         = "rate_limited"
	OutcomeInternal            = "internal"
	OutcomeUpstreamUnavailable = "upstream_unavailable"
	OutcomeError               = "error"
)

// requestLogEndpoint values, the closed set request_logs.endpoint accepts —
// a narrower vocabulary than httpapi's own endpointFor, which also covers
// job-related routes that STATUS.md's P09/C28 explicitly excludes (they
// already have J07's persisted history).
//
// requestLogEndpoint 取值，是 request_logs.endpoint 所接受的封闭集合——比
// httpapi 自己的 endpointFor 更窄，后者还覆盖了 STATUS.md P09/C28 明确排除
// 的 job 相关路由(它们已经有 J07 的持久化历史)。
const (
	requestLogEndpointModels     = "models"
	requestLogEndpointChat       = "chat"
	requestLogEndpointEmbeddings = "embeddings"
	requestLogEndpointResponses  = "responses"
)

// outcomeForStatus maps an HTTP status code onto the closed Outcome
// vocabulary. It is a pure function of the status code alone, precisely so
// that no business handler (chat.go, responses.go, embeddings.go, models.go)
// needs to change to support request search — see the design doc's
// "零改动业务 handler" decision.
//
// outcomeForStatus 把一个 HTTP 状态码映射到封闭的 Outcome 词汇表上。它是一个
// 只依赖状态码本身的纯函数，这正是为了让任何业务 handler(chat.go、
// responses.go、embeddings.go、models.go)都无需为支持请求检索而改动——见
// 设计文档"零改动业务 handler"的决定。
func outcomeForStatus(status int) string {
	switch {
	case status >= 200 && status < 300:
		return OutcomeOK
	case status == 400:
		return OutcomeInvalidRequest
	case status == 401:
		return OutcomeUnauthorized
	case status == 403:
		return OutcomeForbidden
	case status == 404:
		return OutcomeNotFound
	case status == 429:
		return OutcomeRateLimited
	case status == 500:
		return OutcomeInternal
	case status == 502 || status == 503 || status == 504:
		return OutcomeUpstreamUnavailable
	default:
		return OutcomeError
	}
}

// requestLogEndpoint reports the closed endpoint value for path, and
// whether path is one of the four routes STATUS.md's P09/C28 covers at all.
// Every other path — job routes, artifact routes, anything unrecognized —
// returns ok=false, which is the middleware's signal to record nothing.
//
// requestLogEndpoint 报告 path 对应的封闭 endpoint 取值，以及 path 是否属于
// STATUS.md P09/C28 覆盖的四条路由之一。其余任何路径——job 路由、产物路由、
// 任何无法识别的路径——都返回 ok=false，这是中间件"不记录"的信号。
func requestLogEndpoint(path string) (string, bool) {
	switch path {
	case "/v1/models":
		return requestLogEndpointModels, true
	case "/v1/chat/completions":
		return requestLogEndpointChat, true
	case "/v1/embeddings":
		return requestLogEndpointEmbeddings, true
	case "/v1/responses":
		return requestLogEndpointResponses, true
	default:
		return "", false
	}
}

// requestLogRecord is one finished, authenticated front-door request,
// ready to be pushed to the control plane. It carries nothing beyond what
// the design doc's field table allows — no request body, no response body,
// no model name.
//
// requestLogRecord 是一条已完成、已鉴权的前门请求，可供推送至控制面。它携带
// 的字段不超出设计文档字段表所允许的范围——没有请求体、没有响应体、没有
// 模型名。
type requestLogRecord struct {
	RequestID  string
	TenantID   string
	KeyDisplay string
	Endpoint   string
	StatusCode int
	Outcome    string
	DurationMS int64
	CreatedAt  time.Time
}
