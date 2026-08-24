package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
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
}

// New returns the front door's http.Handler: GET /v1/models,
// POST /v1/chat/completions (streaming and non-streaming) and
// POST /v1/embeddings, wrapped in request logging and API key
// authentication.
func New(sched *scheduler.Scheduler, cfg Config) http.Handler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	h := &handlers{sched: sched, logger: logger, metrics: newRecorder(cfg.Metrics)}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", h.models)
	mux.HandleFunc("POST /v1/chat/completions", h.chatCompletions)
	mux.HandleFunc("POST /v1/embeddings", h.embeddings)

	auth := newAuthenticator(cfg.Verifier, cfg.APIKeys, logger)
	// Observation wraps authentication rather than the other way round, so a
	// rejected key still counts as a request: a spike of 401s is exactly the
	// kind of thing the request counter exists to make visible.
	//
	// 观测包在鉴权之外而不是之内，因此被拒绝的密钥同样计入请求：401 的尖峰正是请求
	// 计数器要让人看见的那类事情。
	return h.observe(withLogging(logger, auth.middleware(mux)))
}

type handlers struct {
	sched   *scheduler.Scheduler
	logger  *slog.Logger
	metrics *recorder
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
