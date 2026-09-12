package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/workflowview"
	"AIServeWeave/service/aiServeWeaveGateway/objectstore"
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

	// Workflows holds the catalogue of registered workflow templates behind
	// an atomic pointer (P03), so a control-plane sync can hot-swap it
	// without a request ever observing a half-applied registry. Nil leaves
	// the workflow routes mounted but registering nothing, so a submit gets
	// the same 404 as an unknown template rather than a route that vanishes
	// depending on configuration.
	//
	// Workflows 用一个原子指针持有已注册工作流模板的目录（P03），因此控制面同步
	// 可以热替换它，而不会让任何请求观察到一份只换了一半的目录。为 nil 时工作流
	// 路由照常挂载但目录为空，因此提交会得到与「模板不存在」相同的 404，而不是
	// 一条随配置忽隐忽现的路由。
	Workflows *workflow.Handle

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
	// ArtifactStorage persists generated artifact bytes as they are pulled
	// from the node that produced them (STATUS.md's P04), so they remain
	// downloadable after that node disconnects — see the ControlPlane
	// README's known gap for what the in-memory-only path before P04 could
	// not do. Nil disables byte persistence entirely: artifact metadata
	// (filename/subfolder/type) is still reported to the control plane
	// exactly as before, and downloads always pull live from the node,
	// exactly today's behavior.
	//
	// ArtifactStorage 在产物字节从产出它们的节点被拉取时就地持久化它们
	// （STATUS.md 的 P04），使其在该节点断开后依然可下载——P04 之前那条
	// 仅存于内存的路径做不到什么，见 ControlPlane README 的已知缺口。为
	// nil 时完全关闭字节持久化：产物元数据（文件名/子目录/类型）仍会照常
	// 上报给控制面，下载也始终从节点实时拉取，与今天的行为完全一致。
	ArtifactStorage objectstore.Backend
	// ArtifactCopyTimeout bounds one artifact's node-to-storage byte copy.
	// Zero uses DefaultArtifactCopyTimeout. It is deliberately separate from
	// PersistCallTimeout, which bounds the lightweight metadata call to the
	// control plane — a byte copy moves a real file and needs a timeout
	// sized for that, not for a JSON round trip.
	//
	// ArtifactCopyTimeout 限定单个产物「节点到存储」的字节复制时长。为零时
	// 采用 DefaultArtifactCopyTimeout。它刻意与 PersistCallTimeout 分开——
	// 后者限定到控制面的轻量元数据调用，而一次字节复制搬运的是真实文件，
	// 需要一个按此设定、而非按一次 JSON 往返设定的时长。
	ArtifactCopyTimeout time.Duration

	// JobRecoveryClient asks the control plane which non-terminal jobs are
	// bound to a node/runtime this replica can currently reach, so a
	// restarted replica can recover the route bindings its in-memory job
	// table lost (STATUS.md's J06). Nil disables the recoverer entirely — a
	// deployment with no control plane has nothing to recover from, the same
	// degrade JobPersistClient and Verifier already follow.
	//
	// JobRecoveryClient 向控制面询问哪些非终态 job 绑定在本副本此刻够得着的
	// 节点/runtime 上，好让一个重启后的副本能恢复其内存 job 表已经丢失的路由
	// 绑定（STATUS.md 的 J06）。为 nil 时完全关闭恢复器——未部署控制面的环境
	// 没有什么可供恢复，与 JobPersistClient 和 Verifier 已经遵循的同一种退化。
	JobRecoveryClient JobRecoveryClient
	// RecoverInterval is how often the background recoverer sweeps currently
	// connected nodes for jobs to recover. Zero uses DefaultRecoverInterval.
	//
	// RecoverInterval 是后台恢复器扫描当前已连接节点、寻找待恢复 job 的间隔。
	// 为零时采用 DefaultRecoverInterval。
	RecoverInterval time.Duration
	// RecoverConcurrency bounds how many nodes are asked about at once. Zero
	// uses DefaultRecoverConcurrency.
	//
	// RecoverConcurrency 限定同时询问多少个节点。为零时采用
	// DefaultRecoverConcurrency。
	RecoverConcurrency int
	// RecoverCallTimeout bounds a single recovery call to the control plane.
	// Zero uses DefaultRecoverCallTimeout.
	//
	// RecoverCallTimeout 限定单次向控制面发起的恢复调用的时长。为零时采用
	// DefaultRecoverCallTimeout。
	RecoverCallTimeout time.Duration

	// ArtifactCleanupClient reaps expired artifact records and their object
	// storage bytes (STATUS.md's P04). Nil disables the cleanup sweeper
	// entirely — a deployment with no control plane, or one that never
	// configured ArtifactStorage, has nothing to sweep, the same
	// nil-degrades pattern JobPersistClient and Verifier already follow.
	//
	// ArtifactCleanupClient 回收已过期的产物记录及其对象存储字节
	// （STATUS.md 的 P04）。为 nil 时完全关闭清理扫描器——未部署控制面、
	// 或从未配置 ArtifactStorage 的部署没有什么可供清理，与 JobPersistClient
	// 和 Verifier 已经遵循的同一种「为 nil 时退化」模式。
	ArtifactCleanupClient ArtifactCleanupClient
	// ArtifactCleanupInterval is how often the cleanup sweeper checks for
	// expired artifacts. Zero uses DefaultArtifactCleanupInterval.
	//
	// ArtifactCleanupInterval 是清理扫描器检查过期产物的间隔。为零时采用
	// DefaultArtifactCleanupInterval。
	ArtifactCleanupInterval time.Duration
	// ArtifactRetention is how long a persisted "output" artifact is kept
	// before the sweeper reaps it. Zero uses DefaultArtifactRetention.
	//
	// ArtifactRetention 是一个已持久化的 "output" 产物在被扫描器回收之前
	// 保留多久。为零时采用 DefaultArtifactRetention。
	ArtifactRetention time.Duration
	// ArtifactPreviewRetention is how long a persisted "temp" (preview)
	// artifact is kept — deliberately much shorter than ArtifactRetention,
	// see artifactcleanup.go's package constants. Zero uses
	// DefaultArtifactPreviewRetention.
	//
	// ArtifactPreviewRetention 是一个已持久化的 "temp"（预览）产物保留多久——
	// 刻意比 ArtifactRetention 短得多，见 artifactcleanup.go 的包常量。为零时
	// 采用 DefaultArtifactPreviewRetention。
	ArtifactPreviewRetention time.Duration

	// AllowedUploadExtensions is the file-extension allowlist (dot-prefixed,
	// case-insensitive) submitRun enforces on a workflow's InputFile parts
	// (STATUS.md's P04). Empty uses DefaultAllowedUploadExtensions — there is
	// no way to disable the check entirely, the same as MaxWorkflowUploadBytes
	// a few lines below it in jobs.go. See uploadformat.go for the byte-sniff
	// check layered on top of this filename check.
	//
	// AllowedUploadExtensions 是 submitRun 对工作流 InputFile 分片强制执行的
	// 文件扩展名允许列表（带前导点、大小写不敏感，STATUS.md 的 P04）。为空时
	// 采用 DefaultAllowedUploadExtensions——没有办法完全关闭这项检查，与
	// jobs.go 里几行之外的 MaxWorkflowUploadBytes 一样。叠加在这层文件名检查
	// 之上的字节嗅探检查见 uploadformat.go。
	AllowedUploadExtensions []string
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
		sched:                   sched,
		logger:                  logger,
		metrics:                 newRecorder(cfg.Metrics),
		workflows:               cfg.Workflows,
		jobs:                    newJobStore(cfg.MaxJobs),
		clock:                   clock,
		limiter:                 cfg.Limiter,
		storage:                 cfg.ArtifactStorage,
		allowedUploadExtensions: normalizeAllowedExtensions(cfg.AllowedUploadExtensions),
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
		persister = newJobPersister(h.jobs, cfg.JobPersistClient, sched, cfg.ArtifactStorage, clock, logger, jobPersistConfig{
			Interval:            cfg.PersistInterval,
			BatchSize:           cfg.PersistBatchSize,
			Concurrency:         cfg.PersistConcurrency,
			CallTimeout:         cfg.PersistCallTimeout,
			MaxBackoff:          cfg.PersistMaxBackoff,
			ArtifactCopyTimeout: cfg.ArtifactCopyTimeout,
		})
		go persister.run()
	}
	h.persister = persister

	// The recoverer follows the same nil-degrades pattern: no control plane
	// configured means nothing to recover non-terminal jobs from, so this
	// replica simply keeps whatever its own in-memory job table already
	// holds — the pre-J06 behavior, not a silently broken one.
	//
	// 恢复器遵循同一种「为 nil 时退化」模式：未配置控制面意味着没有什么可供
	// 恢复非终态 job，本副本因此照旧只保留自己内存 job 表已有的内容——这是
	// J06 之前的行为，不是一种悄悄坏掉的行为。
	var recoverer *jobRecoverer
	if cfg.JobRecoveryClient != nil {
		recoverer = newJobRecoverer(h.jobs, sched, cfg.JobRecoveryClient, clock, logger, jobRecoverConfig{
			Interval:    cfg.RecoverInterval,
			Concurrency: cfg.RecoverConcurrency,
			CallTimeout: cfg.RecoverCallTimeout,
		})
		go recoverer.run()
	}

	// The cleanup sweeper follows the same nil-degrades pattern: no control
	// plane configured to reap through means there is nothing durable to
	// clean up in the first place.
	//
	// 清理扫描器遵循同一种「为 nil 时退化」模式：未配置可供回收的控制面，
	// 意味着从一开始就没有什么持久化的东西需要清理。
	var cleaner *artifactCleaner
	if cfg.ArtifactCleanupClient != nil {
		// Defaulted here, per field, rather than left to
		// newArtifactCleaner's own zero-check on the whole map: that check
		// only fires when RetentionByType is nil, and a map built with a
		// zero cfg.ArtifactRetention would otherwise mean "expire the
		// instant an artifact is created" instead of "use the default".
		//
		// 在这里逐字段补上默认值，而不是留给 newArtifactCleaner 自己对整个
		// map 的零值检查——那个检查只在 RetentionByType 为 nil 时才触发，
		// 一个用零值 cfg.ArtifactRetention 构造出的 map，若不这样处理，
		// 含义就会变成「产物一创建就过期」，而不是「采用默认值」。
		retention, previewRetention := cfg.ArtifactRetention, cfg.ArtifactPreviewRetention
		if retention <= 0 {
			retention = DefaultArtifactRetention
		}
		if previewRetention <= 0 {
			previewRetention = DefaultArtifactPreviewRetention
		}
		cleaner = newArtifactCleaner(cfg.ArtifactCleanupClient, cfg.ArtifactStorage, clock, logger, artifactCleanupConfig{
			Interval: cfg.ArtifactCleanupInterval,
			RetentionByType: map[string]time.Duration{
				"output": retention,
				"temp":   previewRetention,
			},
		})
		go cleaner.run()
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
	return &Server{
		Handler:   h.observe(withLogging(logger, auth.middleware(h.rateLimit(mux)))),
		handlers:  h,
		syncer:    syncer,
		persister: persister,
		recoverer: recoverer,
		cleaner:   cleaner,
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
	recoverer *jobRecoverer
	cleaner   *artifactCleaner
}

// Close stops the background job syncer, persister, recoverer and artifact
// cleanup sweeper, waiting for each one's current round, if any, to finish.
// Call it during shutdown, after the HTTP listener has stopped accepting
// new requests and before the scheduler's underlying tunnel is torn down —
// the syncer and the recoverer both dispatch through that same scheduler,
// and stopping them first avoids a burst of "node is not connected"
// warnings against a tunnel that is closing on purpose rather than one that
// failed. The persister and the cleanup sweeper do not dispatch through the
// tunnel at all — they talk to the control plane (and, for the sweeper,
// object storage) — but stopping them here too means shutdown has one
// place that waits for every background loop this package started, not
// four.
//
// It does not stop the HTTP handler itself; that remains the caller's
// http.Server to shut down.
//
// Close 停止后台 job 同步器、持久化器、恢复器与产物清理扫描器，并分别等待
// 它们正在进行的一轮（如果有）跑完。应当在关闭期间调用它——在 HTTP 监听器
// 停止接受新请求之后、调度器底下的隧道被拆除之前——同步器与恢复器都经由
// 同一个调度器分派，先停止它们能避免对着一条正在有意关闭而非故障的隧道
// 打出一串「node is not connected」告警。持久化器与清理扫描器根本不经由
// 隧道分派——它们对话的是控制面（清理扫描器还对话对象存储）——但在这里
// 一并停止它们，意味着关闭流程只有一处要等待本包启动的每一个后台循环，
// 而不是四处。
//
// 它不会停止 HTTP 处理器本身；那仍然是调用方自己的 http.Server 该做的关闭。
func (s *Server) Close() {
	s.syncer.Stop()
	if s.persister != nil {
		s.persister.Stop()
	}
	if s.recoverer != nil {
		s.recoverer.Stop()
	}
	if s.cleaner != nil {
		s.cleaner.Stop()
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
	workflows *workflow.Handle
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
	// requestLogs is nil when no control plane is configured to push
	// request-log records to, in which case requestLogMiddleware is a pure
	// pass-through — the same nil-degrades pattern persister already
	// follows.
	//
	// requestLogs 在未配置可供推送请求记录的控制面时为 nil，此时
	// requestLogMiddleware 是纯粹的透传——与 persister 已经遵循的同一种
	// 「为 nil 时退化」模式。
	requestLogs requestLogSink
	// storage is nil when no Config.ArtifactStorage is configured, in which
	// case downloadArtifact pulls live from the node exactly as it always
	// has. See jobpersist.go's persistArtifacts for the write side.
	//
	// storage 在未配置 Config.ArtifactStorage 时为 nil，此时 downloadArtifact
	// 照旧从节点实时拉取。写入侧见 jobpersist.go 的 persistArtifacts。
	storage objectstore.Backend
	// allowedUploadExtensions is Config.AllowedUploadExtensions normalized
	// into a lookup set — see uploadformat.go.
	//
	// allowedUploadExtensions 是归一化成查找集合后的
	// Config.AllowedUploadExtensions——见 uploadformat.go。
	allowedUploadExtensions map[string]struct{}
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
