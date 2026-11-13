// requestlog.go is the Gateway's side of STATUS.md's P09/C28: it classifies
// a finished, authenticated front-door request into the closed vocabulary
// the design doc requires, and defines the record shape the background
// pusher (requestlogpush.go) batches to the control plane. A successful
// request records only its path, status and timing. A failed one (status 400
// and up) also records, for troubleshooting, the model it asked for, the
// error code and message the client was answered with, and — on dispatch
// failures — the server-side error behind that answer, each truncated to a
// fixed length. No request or response body beyond the error answer itself is
// ever read.
//
// requestlog.go 是 Gateway 一侧对 STATUS.md P09/C28 的实现：把一次已完成、
// 已鉴权的前门请求，分类进设计文档要求的封闭词汇表，并定义后台推送器
// (requestlogpush.go)批量发往控制面所用的记录形状。成功的请求只记录路径、
// 状态码与耗时。失败的请求（状态码 400 及以上）为便于排错另外记录：所请求的
// 模型、答复客户端的错误码与错误信息，以及派发失败时这一答复背后的服务端错误，
// 各自截断到固定长度。除错误答复本身外，从不读取任何请求体或响应体。
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
	"unicode/utf8"

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
// one per inference front door. It is narrower than httpapi's own
// endpointFor, which also covers job-related routes that STATUS.md's P09/C28
// explicitly excludes (they already have J07's persisted history) and the
// Ollama model-management routes, which never dispatch. Values are short
// rather than copied from endpointFor or the usage ledger because the column
// is VARCHAR(16): anthropic_messages would not fit, and widening it is a
// migration this vocabulary does not need.
//
// requestLogEndpoint 取值，是 request_logs.endpoint 所接受的封闭集合——每个
// 推理前门一个。它比 httpapi 自己的 endpointFor 更窄，后者还覆盖了 STATUS.md
// P09/C28 明确排除的 job 相关路由(它们已经有 J07 的持久化历史)，以及从不派发
// 的 Ollama 模型管理路由。取值刻意写短，而不是照搬 endpointFor 或用量账本的
// 名字，因为该列是 VARCHAR(16)：anthropic_messages 放不下，而为这份词汇表
// 加宽列是一次不必要的迁移。
const (
	requestLogEndpointModels         = "models"
	requestLogEndpointChat           = "chat"
	requestLogEndpointEmbeddings     = "embeddings"
	requestLogEndpointResponses      = "responses"
	requestLogEndpointImages         = "images"
	requestLogEndpointMessages       = "messages"
	requestLogEndpointTranscriptions = "transcriptions"
	requestLogEndpointTranslations   = "translations"
	requestLogEndpointRerank         = "rerank"
	requestLogEndpointOllamaChat     = "ollama_chat"
	requestLogEndpointOllamaGenerate = "ollama_generate"
	requestLogEndpointOllamaEmbed    = "ollama_embed"
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
// whether path is one of the inference front doors request search covers at
// all. Every other path — job routes, artifact routes, Ollama model
// management, anything unrecognized — returns ok=false, which is the
// middleware's signal to record nothing.
//
// requestLogEndpoint 报告 path 对应的封闭 endpoint 取值，以及 path 是否属于
// 请求检索覆盖的推理前门之一。其余任何路径——job 路由、产物路由、Ollama 模型
// 管理路由、任何无法识别的路径——都返回 ok=false，这是中间件"不记录"的信号。
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
	case "/v1/images/generations":
		return requestLogEndpointImages, true
	case "/v1/messages":
		return requestLogEndpointMessages, true
	case "/v1/audio/transcriptions":
		return requestLogEndpointTranscriptions, true
	case "/v1/audio/translations":
		return requestLogEndpointTranslations, true
	case "/v1/rerank":
		return requestLogEndpointRerank, true
	case "/api/chat":
		return requestLogEndpointOllamaChat, true
	case "/api/generate":
		return requestLogEndpointOllamaGenerate, true
	case "/api/embeddings":
		return requestLogEndpointOllamaEmbed, true
	default:
		return "", false
	}
}

// RequestLogRecord is one finished, authenticated front-door request,
// ready to be pushed to the control plane. It never carries a request or
// response body. Model, ErrorCode, ErrorMessage and ErrorDetail are set only
// for a failed request (status 400 and up) and are empty otherwise, so a
// successful request costs no more than it did before they existed. It is
// exported specifically so controlplaneclient's RequestLogsClient — an
// implementation of RequestLogClient living outside this package — can be
// typed against it.
//
// RequestLogRecord 是一条已完成、已鉴权的前门请求，可供推送至控制面。它从不
// 携带请求体或响应体。Model、ErrorCode、ErrorMessage 与 ErrorDetail 只对失败
// 请求（状态码 400 及以上）设置，其余情况为空，因此成功请求的开销与这些字段
// 出现之前相同。它被特意导出，正是为了让本包之外、RequestLogClient 的实现方
// controlplaneclient 的 RequestLogsClient 能够以它为类型。
type RequestLogRecord struct {
	RequestID  string
	TenantID   string
	KeyDisplay string
	Endpoint   string
	StatusCode int
	Outcome    string
	DurationMS int64
	CreatedAt  time.Time
	// Model is the model the client asked for, as sent.
	// Model 是客户端请求的模型，按原样记录。
	Model string
	// ErrorCode and ErrorMessage are what the client was answered with: the
	// classification code (model_not_found, internal_error, a runtime error
	// code, …) and the error body's message.
	//
	// ErrorCode 与 ErrorMessage 是答复客户端的内容：分类错误码（model_not_found、
	// internal_error、某个运行时错误码……）与错误体中的信息。
	ErrorCode    string
	ErrorMessage string
	// ErrorDetail is the server-side error behind a dispatch failure, which
	// the client never sees — "internal error" alone says nothing about what
	// broke. Only the operator search shows it.
	//
	// ErrorDetail 是派发失败背后的服务端错误，客户端看不到——单凭 "internal
	// error" 说明不了哪里坏了。只有运维检索会显示它。
	ErrorDetail string
}

// Length limits for the failure fields, matching the request_logs columns
// they are stored in. Values are cut on a rune boundary.
//
// 失败字段的长度上限，与存放它们的 request_logs 列一致。按 rune 边界截断。
const (
	requestLogModelMax        = 128
	requestLogErrorCodeMax    = 64
	requestLogErrorMessageMax = 512
	requestLogErrorDetailMax  = 1024
	// requestLogErrorBodyMax bounds how much of an error response is kept to
	// read its code and message from; every error body this package writes
	// is far smaller.
	// requestLogErrorBodyMax 限定为读取错误码与错误信息而保留的错误响应长度；本包
	// 写出的任何错误体都远小于它。
	requestLogErrorBodyMax = 4096
)

// requestLogNote collects the failure context of one request while it is
// served: handlers record the model they parsed and the dispatch error they
// classified, and the middleware reads it back only if the request failed.
//
// requestLogNote 在请求服务期间收集它的失败上下文：handler 记下解析出的模型与
// 归类的派发错误，中间件只在请求失败时读回。
type requestLogNote struct {
	model     string
	errorCode string
	detail    string
}

type requestLogNoteKey struct{}

// noteRequestModel records the model a request asked for. It is a no-op
// when the request is not being logged.
//
// noteRequestModel 记下请求所要的模型。请求未被记录时为空操作。
func noteRequestModel(ctx context.Context, model string) {
	if note, ok := ctx.Value(requestLogNoteKey{}).(*requestLogNote); ok && note.model == "" {
		note.model = model
	}
}

// noteDispatchError records the classification code and the server-side
// error of a dispatch failure on w's request log, if w is (or wraps) the
// request-log writer. The first failure noted wins: it is the one the client
// was answered with.
//
// noteDispatchError 在 w 是（或包装了）请求日志 writer 时，把一次派发失败的分类
// 错误码与服务端错误记到它的请求日志上。以第一次记下的失败为准：那正是答复客户端
// 的那一次。
func noteDispatchError(w http.ResponseWriter, code string, err error) {
	for w != nil {
		if lw, ok := w.(*requestLogWriter); ok {
			if lw.note.errorCode == "" && lw.note.detail == "" {
				lw.note.errorCode = code
				if err != nil {
					lw.note.detail = err.Error()
				}
			}
			return
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = u.Unwrap()
	}
}

// requestLogWriter is the statusWriter requestLogMiddleware serves through.
// On top of the status it keeps the beginning of an error response, so the
// code and message the client received can be recorded without any handler
// having to report them.
//
// requestLogWriter 是 requestLogMiddleware 所用的 statusWriter。除状态码外，它还
// 保留错误响应的开头，使客户端收到的错误码与错误信息无需任何 handler 上报就能记录。
type requestLogWriter struct {
	*statusWriter
	note *requestLogNote
	body []byte
}

// Write keeps the first requestLogErrorBodyMax bytes of an error response.
// Write 保留错误响应的前 requestLogErrorBodyMax 字节。
func (w *requestLogWriter) Write(b []byte) (int, error) {
	if statusOf(w.statusWriter) >= http.StatusBadRequest && len(w.body) < requestLogErrorBodyMax {
		w.body = append(w.body, b[:min(len(b), requestLogErrorBodyMax-len(w.body))]...)
	}
	return w.statusWriter.Write(b)
}

// errorBodyFields reads the code and message out of an error body in any of
// the shapes this package writes: OpenAI ({"error":{"code","type",
// "message"}}), Anthropic ({"type":"error","error":{"type","message"}}) and
// Ollama ({"error":"message"}). Without a code the error type stands in. An
// unrecognized body yields empty strings.
//
// errorBodyFields 从本包写出的任一形状的错误体中读取错误码与错误信息：OpenAI、
// Anthropic 与 Ollama。没有错误码时以错误类型代替。无法识别的错误体返回空字符串。
func errorBodyFields(body []byte) (code, message string) {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Error) == 0 {
		return "", ""
	}
	var text string
	if json.Unmarshal(envelope.Error, &text) == nil {
		return "", text
	}
	var detail struct {
		Code    any    `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if json.Unmarshal(envelope.Error, &detail) != nil {
		return "", ""
	}
	code = detail.Type
	if c, ok := detail.Code.(string); ok && c != "" {
		code = c
	}
	return code, detail.Message
}

// truncateRunes cuts s to at most limit bytes on a rune boundary.
// truncateRunes 在 rune 边界上把 s 截断到最多 limit 字节。
func truncateRunes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
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
	enqueue(RequestLogRecord) bool
}

// requestLogMiddleware records one RequestLogRecord per finished request
// that both resolved a tenant identity (auth.middleware already ran) and
// matches one of the front doors requestLogEndpoint covers. It is placed
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
// 又匹配 requestLogEndpoint 覆盖的前门之一的、已完成的请求，记录一条
// RequestLogRecord。它被特意放在链路中 auth.middleware 之后，好让这段代码
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
		note := &requestLogNote{}
		sw := &requestLogWriter{statusWriter: &statusWriter{ResponseWriter: w, status: http.StatusOK}, note: note}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), requestLogNoteKey{}, note)))
		status := statusOf(sw.statusWriter)

		var keyDisplay string
		if key, ok := requestKey(r); ok {
			keyDisplay = apikey.Display(key)
		}
		record := RequestLogRecord{
			RequestID:  requestIDFrom(r.Context()),
			TenantID:   identity.TenantID,
			KeyDisplay: keyDisplay,
			Endpoint:   endpoint,
			StatusCode: status,
			Outcome:    outcomeForStatus(status),
			DurationMS: h.clock.Now().Sub(start).Milliseconds(),
			CreatedAt:  start,
		}
		if status >= http.StatusBadRequest {
			code, message := errorBodyFields(sw.body)
			if note.errorCode != "" {
				code = note.errorCode
			}
			record.Model = truncateRunes(note.model, requestLogModelMax)
			record.ErrorCode = truncateRunes(code, requestLogErrorCodeMax)
			record.ErrorMessage = truncateRunes(message, requestLogErrorMessageMax)
			record.ErrorDetail = truncateRunes(note.detail, requestLogErrorDetailMax)
		}
		accepted := h.requestLogs.enqueue(record)
		if !accepted {
			h.metrics.RequestLogDropped()
		}
	})
}
