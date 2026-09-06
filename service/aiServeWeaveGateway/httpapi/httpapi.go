package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/workflowview"
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

	// Clock stamps job timestamps and drives the background job syncer below.
	// Nil uses the system clock; tests inject a fake so a job's timeline —
	// and the syncer's ticks — are asserted without sleeping.
	//
	// Clock 为 job 的时间戳提供时间，也驱动下面的后台 job 同步器。为 nil 时使用
	// 系统时钟；测试注入假时钟，好在不睡眠的前提下断言 job 的时间线与同步器的节拍。
	Clock runtime.Clock

	// SyncInterval is how often the background syncer sweeps for non-terminal
	// jobs to ask about. Zero uses DefaultSyncInterval.
	//
	// SyncInterval 是后台同步器扫描非终态 job 并询问它们的间隔。为零时采用
	// DefaultSyncInterval。
	SyncInterval time.Duration
	// SyncBatchSize bounds how many jobs one sweep considers. Zero uses
	// DefaultSyncBatchSize.
	//
	// SyncBatchSize 限定一次扫描考虑多少个 job。为零时采用 DefaultSyncBatchSize。
	SyncBatchSize int
	// SyncConcurrency bounds how many of those jobs are asked about at once.
	// Zero uses DefaultSyncConcurrency.
	//
	// SyncConcurrency 限定其中同时被询问的个数。为零时采用 DefaultSyncConcurrency。
	SyncConcurrency int
	// SyncCallTimeout bounds a single background status call. Zero uses
	// DefaultSyncCallTimeout.
	//
	// SyncCallTimeout 限定单次后台状态调用的时长。为零时采用 DefaultSyncCallTimeout。
	SyncCallTimeout time.Duration
	// SyncMaxBackoff caps how long a repeatedly failing job waits between
	// background attempts. Zero uses DefaultSyncMaxBackoff.
	//
	// SyncMaxBackoff 限定一个反复失败的 job 在后台尝试之间最多等待多久。为零时采用
	// DefaultSyncMaxBackoff。
	SyncMaxBackoff time.Duration

	// JobPersistClient writes job records to the control plane's internal
	// Job API (STATUS.md's J04/J05). Nil disables the persister entirely —
	// a deployment with no control plane gets Gateway-local job tracking
	// only, the same degrade a nil Verifier already implies for API keys.
	//
	// JobPersistClient 把 job 记录写入控制面的内部 Job API（STATUS.md 的
	// J04/J05）。为 nil 时完全关闭持久化器——未部署控制面的环境只得到
	// Gateway 本地的 job 跟踪，与 nil Verifier 对 API Key 已经隐含的退化
	// 相同。
	JobPersistClient JobPersistClient
	// PersistInterval is how often the background persister sweeps for jobs
	// whose control-plane record is missing or behind. Zero uses
	// DefaultPersistInterval.
	//
	// PersistInterval 是后台持久化器扫描控制面记录缺失或落后的 job 的间隔。
	// 为零时采用 DefaultPersistInterval。
	PersistInterval time.Duration
	// PersistBatchSize bounds how many jobs one sweep writes. Zero uses
	// DefaultPersistBatchSize.
	//
	// PersistBatchSize 限定一次扫描写入多少个 job。为零时采用
	// DefaultPersistBatchSize。
	PersistBatchSize int
	// PersistConcurrency bounds how many of those writes are in flight at
	// once. Zero uses DefaultPersistConcurrency.
	//
	// PersistConcurrency 限定其中同时在途的写入个数。为零时采用
	// DefaultPersistConcurrency。
	PersistConcurrency int
	// PersistCallTimeout bounds a single write to the control plane. Zero
	// uses DefaultPersistCallTimeout.
	//
	// PersistCallTimeout 限定单次写入控制面的时长。为零时采用
	// DefaultPersistCallTimeout。
	PersistCallTimeout time.Duration
	// PersistMaxBackoff caps how long a repeatedly failing job waits between
	// persistence attempts. Zero uses DefaultPersistMaxBackoff.
	//
	// PersistMaxBackoff 限定一个反复失败的 job 在持久化尝试之间最多等待多久。
	// 为零时采用 DefaultPersistMaxBackoff。
	PersistMaxBackoff time.Duration
}

// New returns the front door's http.Handler: GET /v1/models,
// POST /v1/chat/completions (streaming and non-streaming),
// POST /v1/embeddings, POST /v1/workflows/{workflow_id}/runs,
// GET /v1/jobs/{job_id}, GET /v1/jobs/{job_id}/events (SSE),
// POST /v1/jobs/{job_id}/cancel, GET /v1/jobs/{job_id}/artifacts and
// GET /v1/artifacts/{artifact_id}, wrapped in request logging and API key
// authentication.
func New(sched *scheduler.Scheduler, cfg Config) *Server {
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

	syncer := newJobSyncer(h.jobs, sched, clock, logger, jobSyncConfig{
		Interval:    cfg.SyncInterval,
		BatchSize:   cfg.SyncBatchSize,
		Concurrency: cfg.SyncConcurrency,
		CallTimeout: cfg.SyncCallTimeout,
		MaxBackoff:  cfg.SyncMaxBackoff,
	})
	go syncer.run()

	// The persister only exists when a control plane is configured to write
	// job records to — the same nil-degrades pattern cfg.Verifier already
	// follows for API keys. A deployment with none gets Gateway-local job
	// tracking only, honestly, rather than a background loop that starts
	// and immediately has nothing it can do.
	//
	// 持久化器只在配置了可写入 job 记录的控制面时才存在——与 cfg.Verifier 对
	// API Key 已经遵循的同一种「为 nil 时退化」模式。未配置的部署如实得到
	// 仅限 Gateway 本地的 job 跟踪，而不是一个启动起来却什么都做不了的后台
	// 循环。
	var persister *jobPersister
	if cfg.JobPersistClient != nil {
		persister = newJobPersister(h.jobs, cfg.JobPersistClient, clock, logger, jobPersistConfig{
			Interval:    cfg.PersistInterval,
			BatchSize:   cfg.PersistBatchSize,
			Concurrency: cfg.PersistConcurrency,
			CallTimeout: cfg.PersistCallTimeout,
			MaxBackoff:  cfg.PersistMaxBackoff,
		})
		go persister.run()
	}
	h.persister = persister

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
	return &Server{
		Handler:   h.observe(withLogging(logger, auth.middleware(h.rateLimit(mux)))),
		handlers:  h,
		syncer:    syncer,
		persister: persister,
	}
}

// Server is the front door, plus a read-only window onto the job table.
//
// The window exists so the operator listener can report the runs this replica
// is holding without going through the front door — that path requires a
// tenant's API key, which no operator has and none should need to read their
// own deployment's state. It is deliberately narrow: one method, taking the
// tenant it is scoped to, returning copies.
//
// Server 是前门，外加一个对 job 表的只读窗口。
//
// 这个窗口的存在，是为了让运维监听器无需经由前门就能报告本副本持有的运行——那条路径
// 需要租户的 API Key，而运维并没有，也不该为了读自己部署的状态而需要它。它刻意很窄：
// 一个方法，接收它所限定的租户，返回副本。
type Server struct {
	http.Handler
	handlers  *handlers
	syncer    *jobSyncer
	persister *jobPersister
}

// Close stops the background job syncer and the job persister, waiting for
// each one's current round, if any, to finish. Call it during shutdown,
// after the HTTP listener has stopped accepting new requests and before the
// scheduler's underlying tunnel is torn down — the syncer dispatches
// through that same scheduler, and stopping it first avoids a burst of
// "node is not connected" warnings against a tunnel that is closing on
// purpose rather than one that failed. The persister does not dispatch
// through the tunnel at all — it talks to the control plane — but stopping
// it here too means shutdown has one place that waits for every background
// loop this package started, not two.
//
// It does not stop the HTTP handler itself; that remains the caller's
// http.Server to shut down.
//
// Close 停止后台 job 同步器与 job 持久化器，并分别等待它们正在进行的一轮
// （如果有）跑完。应当在关闭期间调用它——在 HTTP 监听器停止接受新请求之后、
// 调度器底下的隧道被拆除之前——同步器经由同一个调度器分派，先停止它能避免
// 对着一条正在有意关闭而非故障的隧道打出一串「node is not connected」告警。
// 持久化器根本不经由隧道分派——它对话的是控制面——但在这里一并停止它，
// 意味着关闭流程只有一处要等待本包启动的每一个后台循环，而不是两处。
//
// 它不会停止 HTTP 处理器本身；那仍然是调用方自己的 http.Server 该做的关闭。
func (s *Server) Close() {
	s.syncer.Stop()
	if s.persister != nil {
		s.persister.Stop()
	}
}

// JobsFor returns this replica's runs for one tenant, newest first, and
// whether the table has dropped older runs to stay within its bound.
//
// The tenant id is a parameter rather than a filter applied afterwards: this
// table holds every tenant's runs, and a caller that could ask for all of
// them would be one forgotten argument away from a cross-tenant read.
//
// JobsFor 返回本副本上某一个租户的运行，最新的在前，并报告该表是否为守住上限而丢弃过
// 较早的运行。
//
// 租户 id 是参数而不是事后施加的过滤：这张表持有每个租户的运行，而一个能索取全部的
// 调用方，距离一次跨租户读取只差一个被遗忘的实参。
func (s *Server) JobsFor(tenantID string) ([]workflowview.Job, bool) {
	return s.handlers.jobs.forTenant(tenantID)
}

// Templates returns the registered workflow catalogue, without the graphs.
//
// Templates 返回已注册的工作流目录，不含图。
func (s *Server) Templates() []workflowview.Template {
	return renderTemplates(s.handlers.workflows)
}

type handlers struct {
	sched     *scheduler.Scheduler
	logger    *slog.Logger
	metrics   *recorder
	workflows *workflow.Registry
	jobs      *jobStore
	clock     runtime.Clock
	limiter   ratelimit.Limiter
	// persister is nil when no JobPersistClient is configured. Its nudge
	// method is nil-receiver-safe, so call sites never need to check this
	// for nil themselves — see jobpersist.go.
	//
	// persister 在未配置 JobPersistClient 时为 nil。它的 nudge 方法对 nil
	// 接收者是安全的，因此调用点从不需要自己检查它是否为 nil——见 jobpersist.go。
	persister *jobPersister
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
