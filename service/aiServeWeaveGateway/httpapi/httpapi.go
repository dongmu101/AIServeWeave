package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/ratelimit"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/workflow"
)

// Config configures the HTTP front door.
type Config struct {
	// Verifier resolves API keys against the control plane. When set it is
	// the authority and APIKeys is ignored; see auth.go for the three modes.
	//
	// Verifier 对着控制面解析 API Key。设置了它时以它为准，APIKeys 会被忽略；
	// 三种模式见 auth.go。
	Verifier KeyVerifier
	// APIKeys is the static set of accepted Bearer tokens, used when no
	// Verifier is configured. Empty means authentication is disabled — see
	// auth.go's warning about that.
	APIKeys []string
	// Logger receives one line per request (method, path, status, duration,
	// request_id, model) and one line per dispatch failure. It never
	// receives message or embedding input content.
	Logger *slog.Logger

	// Metrics receives the front door's instruments, described by
	// Descriptions. Nil discards them.
	//
	// Metrics 接收前门的仪器，其描述见 Descriptions。为 nil 时全部丢弃。
	Metrics runtime.Metrics

	// Workflows is the catalogue of registered workflow templates. Nil leaves
	// the workflow routes mounted but registering nothing, so a submit gets
	// the same 404 as an unknown template rather than a route that vanishes
	// depending on configuration.
	//
	// Workflows 是已注册工作流模板的目录。为 nil 时工作流路由照常挂载但目录为空，
	// 因此提交会得到与「模板不存在」相同的 404，而不是一条随配置忽隐忽现的路由。
	Workflows *workflow.Registry

	// MaxJobs bounds the in-memory job table. Zero uses DefaultMaxJobs.
	//
	// MaxJobs 限制内存 job 表的大小。为零时采用 DefaultMaxJobs。
	MaxJobs int

	// Limiter enforces each tenant's quota.Limits. Nil disables enforcement
	// entirely, which is what a deployment with no control plane gets: there
	// are no limits to enforce when there is nothing issuing them.
	//
	// Limiter 执行每个租户的 quota.Limits。为 nil 时完全关闭执行，未部署控制面的
	// 环境得到的正是这个：没有东西签发限制时，也就没有限制可执行。
	Limiter ratelimit.Limiter

	// Clock stamps job timestamps. Nil uses the system clock; tests inject a
	// fake so a job's timeline is asserted without sleeping.
	//
	// Clock 为 job 的时间戳提供时间。为 nil 时使用系统时钟；测试注入假时钟，好在
	// 不睡眠的前提下断言 job 的时间线。
	Clock runtime.Clock
}

// New returns the front door's http.Handler: GET /v1/models,
// POST /v1/chat/completions (streaming and non-streaming),
// POST /v1/embeddings, POST /v1/workflows/{workflow_id}/runs,
// GET /v1/jobs/{job_id}, GET /v1/jobs/{job_id}/events (SSE),
// POST /v1/jobs/{job_id}/cancel, GET /v1/jobs/{job_id}/artifacts and
// GET /v1/artifacts/{artifact_id}, wrapped in request logging and API key
// authentication.
func New(sched *scheduler.Scheduler, cfg Config) http.Handler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	clock := cfg.Clock
	if clock == nil {
		clock = runtime.NewSystemClock()
	}

	h := &handlers{
		sched:     sched,
		logger:    logger,
		metrics:   newRecorder(cfg.Metrics),
		workflows: cfg.Workflows,
		jobs:      newJobStore(cfg.MaxJobs),
		clock:     clock,
		limiter:   cfg.Limiter,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", h.models)
	mux.HandleFunc("POST /v1/chat/completions", h.chatCompletions)
	mux.HandleFunc("POST /v1/embeddings", h.embeddings)
	mux.HandleFunc("POST /v1/responses", h.responses)
	mux.HandleFunc("POST /v1/workflows/{workflow_id}/runs", h.submitRun)
	mux.HandleFunc("GET /v1/jobs/{job_id}", h.jobStatus)
	mux.HandleFunc("GET /v1/jobs/{job_id}/events", h.jobEvents)
	mux.HandleFunc("POST /v1/jobs/{job_id}/cancel", h.cancelJob)
	mux.HandleFunc("GET /v1/jobs/{job_id}/artifacts", h.listArtifacts)
	mux.HandleFunc("GET /v1/artifacts/{artifact_id}", h.downloadArtifact)

	auth := newAuthenticator(cfg.Verifier, cfg.APIKeys, logger)
	// Observation wraps authentication rather than the other way round, so a
	// rejected key still counts as a request: a spike of 401s is exactly the
	// kind of thing the request counter exists to make visible.
	//
	// 观测包在鉴权之外而不是之内，因此被拒绝的密钥同样计入请求：401 的尖峰正是请求
	// 计数器要让人看见的那类事情。
	// The limiter sits inside authentication and outside the routes: there is
	// no tenant to enforce against until the key has been resolved, and every
	// route is subject to the quota once there is one.
	//
	// 限流器坐在鉴权内侧、路由外侧：在 key 被解析出来之前没有可执行的租户，而一旦有了
	// 租户，每条路由都受配额约束。
	return h.observe(withLogging(logger, auth.middleware(h.rateLimit(mux))))
}

type handlers struct {
	sched     *scheduler.Scheduler
	logger    *slog.Logger
	metrics   *recorder
	workflows *workflow.Registry
	jobs      *jobStore
	clock     runtime.Clock
	limiter   ratelimit.Limiter
}

// observe wraps the whole handler chain in the request counter, the duration
// histogram and the in-flight gauge.
//
// observe 把整条处理链包进请求计数器、时长直方图与并发量表。
func (h *handlers) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		endpoint := endpointFor(r.URL.Path)
		finish := h.metrics.RequestStarted(endpoint)

		sw := &statusWriter{ResponseWriter: w}
		defer func() { finish(statusOf(sw), time.Since(start)) }()
		next.ServeHTTP(sw, r)
	})
}

// withLogging assigns each request a request_id, echoes it back in
// X-Request-Id, and logs method, path, status and duration once the
// response is complete. It never logs a request body or the model's output.
func withLogging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		requestID := newRequestID()
		w.Header().Set("X-Request-Id", requestID)
		ctx := withRequestID(r.Context(), requestID)
		r = r.WithContext(ctx)

		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)

		logger.Info("request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", sw.status),
			slog.Duration("duration", time.Since(start)),
			slog.String("request_id", requestID))
	})
}

// statusWriter captures the status code a handler writes, since
// http.ResponseWriter does not expose it after the fact.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status = status
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wroteHeader = true
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read only fails if the OS entropy source is broken,
		// which is not a condition a request-correlation ID should crash
		// the handler over; a fixed sentinel just means correlation is
		// degraded for that one request.
		return "unavailable"
	}
	return hex.EncodeToString(b[:])
}
