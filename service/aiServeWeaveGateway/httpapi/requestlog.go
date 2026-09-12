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
	"net/http"
	"time"

	"AIServeWeave/common/apikey"
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

// requestLogSink is what the middleware hands a finished record to. It is
// satisfied by the bounded background pusher (requestlogpush.go); tests use
// a fake. enqueue reports whether the record was accepted, purely so the
// middleware's own tests can observe the outcome — the middleware itself
// never acts differently on false, since a dropped record is the sink's own
// bounded-buffer policy, not something the middleware retries or escalates.
//
// requestLogSink 是中间件把一条完成的记录交付给的对象。它由有界后台推送器
// (requestlogpush.go)实现；测试中用假实现替代。enqueue 报告该记录是否被
// 接受，纯粹是为了让中间件自己的测试能够观察结果——中间件本身从不因 false
// 而采取不同行动，因为一条记录被丢弃是接收端自己的有界缓冲策略，不是中间件
// 需要重试或上报的事情。
type requestLogSink interface {
	enqueue(requestLogRecord) bool
}

// requestLogMiddleware records one requestLogRecord per finished request
// that both resolved a tenant identity (auth.middleware already ran) and
// matches one of the four routes STATUS.md's P09/C28 covers. It is placed
// after auth.middleware in the chain specifically so IdentityFrom(ctx) is
// already populated when this code runs — see the design doc's "采集链路"
// section for why that ordering avoids any cross-middleware context-sharing
// machinery.
//
// A nil h.requestLogs (no control plane configured to push to) makes this
// middleware a pure pass-through, the same nil-degrades convention every
// other background feature in this package already follows.
//
// requestLogMiddleware 为每一个既解析出了租户身份(auth.middleware 已经跑过)
// 又匹配 STATUS.md P09/C28 覆盖的四条路由之一的、已完成的请求，记录一条
// requestLogRecord。它被特意放在链路中 auth.middleware 之后，好让这段代码
// 运行时 IdentityFrom(ctx) 已经就绪——为什么这个顺序能避免任何跨中间件的
// context 共享机制，见设计文档「采集链路」一节。
//
// h.requestLogs 为 nil(未配置可供推送的控制面)时，本中间件是纯粹的透传，
// 与本包其余每一个后台特性已经遵循的同一种"为 nil 时退化"约定相同。
func (h *handlers) requestLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.requestLogs == nil {
			next.ServeHTTP(w, r)
			return
		}
		endpoint, ok := requestLogEndpoint(r.URL.Path)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		identity, ok := IdentityFrom(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}

		start := h.clock.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		status := statusOf(sw)

		var keyDisplay string
		if key, ok := bearerToken(r.Header.Get("Authorization")); ok {
			keyDisplay = apikey.Display(key)
		}
		accepted := h.requestLogs.enqueue(requestLogRecord{
			RequestID:  requestIDFrom(r.Context()),
			TenantID:   identity.TenantID,
			KeyDisplay: keyDisplay,
			Endpoint:   endpoint,
			StatusCode: status,
			Outcome:    outcomeForStatus(status),
			DurationMS: h.clock.Now().Sub(start).Milliseconds(),
			CreatedAt:  start,
		})
		if !accepted {
			h.metrics.RequestLogDropped()
		}
	})
}
