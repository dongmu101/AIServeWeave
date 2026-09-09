package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"path"
	"sync"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/objectstore"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

// Defaults for the background job persister. They bound how much work one
// replica does writing job records to the control plane: at most
// DefaultPersistBatchSize jobs per DefaultPersistInterval, at most
// DefaultPersistConcurrency of them in flight at once, and no single
// metadata call is allowed to hang past DefaultPersistCallTimeout.
//
// DefaultArtifactCopyTimeout is a separate, much larger bound: it applies
// only to the node-to-storage byte copy persistArtifacts does when
// ArtifactStorage is configured, which moves real file bytes rather than a
// small JSON body and needs a timeout sized for that.
//
// 后台 job 持久化器的默认值。它们限定了一个副本向控制面写入 job 记录最多做多少
// 工作：每 DefaultPersistInterval 至多 DefaultPersistBatchSize 个 job，至多
// DefaultPersistConcurrency 个同时在途，且单次元数据调用不允许挂起超过
// DefaultPersistCallTimeout。
//
// DefaultArtifactCopyTimeout 是一个独立、大得多的上限：它只施加于配置了
// ArtifactStorage 时 persistArtifacts 所做的「节点到存储」字节复制——那搬运的
// 是真实文件字节而非一个小的 JSON 请求体，需要一个按此设定的时长。
const (
	DefaultPersistInterval     = 5 * time.Second
	DefaultPersistBatchSize    = 200
	DefaultPersistConcurrency  = 8
	DefaultPersistCallTimeout  = 3 * time.Second
	DefaultPersistMaxBackoff   = 5 * time.Minute
	DefaultArtifactCopyTimeout = 5 * time.Minute
)

// JobPersistClient is what the background persister needs to write one
// job's record to the control plane's internal Job API (STATUS.md's J04).
//
// It is declared here with primitive-typed parameters rather than by
// importing controlplaneclient's request/response structs, because
// controlplaneclient already imports this package (for Identity and
// KeyVerifier) — the reverse import would be a cycle. controlplaneclient
// provides an adapter satisfying this interface; see its GatewayPersister.
//
// Both methods are expected to behave like the persistence contract in the
// ControlPlane README requires: CreateJob is idempotent on a duplicate id
// for the same tenant, and UpdateJobState is a silent no-op — applied=false,
// err=nil — for a stale ObservedSeq or a job already terminal on the control
// plane's side. This type does not re-implement that distinction; it only
// relies on it.
//
// JobPersistClient 是后台持久化器向控制面内部 Job API（STATUS.md 的 J04）写入
// 一个 job 记录所需的东西。
//
// 这里用原始类型参数声明它，而不是导入 controlplaneclient 的请求/响应结构体，
// 是因为 controlplaneclient 已经导入了本包（用于 Identity 与 KeyVerifier）——
// 反过来导入就会成环。controlplaneclient 提供一个满足本接口的适配器，见它的
// GatewayPersister。
//
// 两个方法都应当表现出 ControlPlane README 的持久化契约所要求的行为：CreateJob
// 对同一租户下重复的 id 是幂等的，UpdateJobState 对一个陈旧的 ObservedSeq 或一个
// 在控制面那一侧已经终态的 job 是无声的空操作——applied=false，err=nil。本类型
// 不重新实现这条区分，只是依赖它。
type JobPersistClient interface {
	// CreateJob reports a run's "已确认" fact. observedSeq is always 0 here:
	// it is the initial state the run was created with, before any later
	// observation.
	//
	// CreateJob 报告一次运行的「已确认」事实。observedSeq 这里始终是 0：它是
	// 这次运行被创建时的初始状态，早于此后的任何观测。
	CreateJob(ctx context.Context, jobID, tenantID, workflowID, workflowVersion, nodeID, runtimeID, backendRunID, state string, observedSeq int64) error
	// UpdateJobState reports one status observation, gated by observedSeq on
	// the control plane's side. applied reports whether this observation
	// was the one that landed; see the type doc comment for why false is
	// not an error here.
	//
	// UpdateJobState 报告一次状态观测，在控制面那一侧以 observedSeq 为放行
	// 条件。applied 报告这次观测是否真正落地；为什么这里 false 不是错误，
	// 见类型的文档注释。
	UpdateJobState(ctx context.Context, tenantID, jobID, state, errorSummary string, observedSeq int64) (applied bool, err error)
	// CreateJobArtifact records one artifact a run produced, using the
	// public id listArtifacts already minted for it. Like CreateJob, a
	// duplicate id for the same job and tenant is not an error.
	//
	// sha256, contentType and storageKey are empty and sizeBytes is 0 when
	// this replica has no ArtifactStorage configured, or has not yet copied
	// this artifact's bytes into it — persistArtifacts calls this the same
	// way either way, so a control plane without the P04 columns applied
	// yet, or a Gateway without object storage configured, both still get
	// the metadata-only record this call always carried before P04.
	//
	// CreateJobArtifact 记录一次运行产出的一个产物，使用 listArtifacts 已经
	// 为它铸造的公开 id。与 CreateJob 一样，同一 job 与租户下重复的 id 不是
	// 错误。
	//
	// 本副本未配置 ArtifactStorage、或尚未把这个产物的字节复制进去时，
	// sha256、contentType、storageKey 为空，sizeBytes 为 0——persistArtifacts
	// 两种情况下都用同一种方式调用本方法，因此无论是尚未套用 P04 新列的控制面，
	// 还是未配置对象存储的 Gateway，得到的都仍是 P04 之前这次调用本就携带的
	// 那份仅有元数据的记录。
	CreateJobArtifact(ctx context.Context, jobID, artifactID, tenantID, filename, subfolder, artifactType, sha256, contentType, storageKey string, sizeBytes int64) error
}

// artifactOpener is the one scheduler method the persister needs to pull an
// artifact's bytes for copying into object storage. It is an interface
// rather than a concrete *scheduler.Scheduler for the same reason
// jobrecover.go's jobRecoverCandidates is: a test can drive it against a
// stub without a tunnel.
//
// artifactOpener 是持久化器为把产物字节复制进对象存储所需要的唯一一个
// scheduler 方法。它是一个接口而不是具体的 *scheduler.Scheduler，理由与
// jobrecover.go 的 jobRecoverCandidates 相同：测试能针对一个桩来驱动它，
// 而无需搭起隧道。
type artifactOpener interface {
	OpenArtifact(ctx context.Context, c scheduler.Candidate, ref runtime.ArtifactRef) (runtime.Artifact, error)
}

// jobPersistConfig collects the persister's tunable bounds, defaulted by
// newJobPersister so callers only need to set what they want to override.
//
// jobPersistConfig 收集持久化器的可调上限，由 newJobPersister 补上默认值，
// 因此调用方只需设置想要覆盖的部分。
type jobPersistConfig struct {
	Interval    time.Duration
	BatchSize   int
	Concurrency int
	CallTimeout time.Duration
	MaxBackoff  time.Duration
	// ArtifactCopyTimeout bounds one artifact's node-to-storage byte copy.
	// See DefaultArtifactCopyTimeout for why it is not CallTimeout.
	//
	// ArtifactCopyTimeout 限定单个产物「节点到存储」的字节复制时长。为何它
	// 不是 CallTimeout，见 DefaultArtifactCopyTimeout。
	ArtifactCopyTimeout time.Duration
}

// jobPersister is the bypass write path from this Gateway replica's in-memory
// job table to the control plane's jobs table, per STATUS.md's J05 and the
// ControlPlane README's 「Job 持久化契约」. It never sits between a submit or
// a status observation and the response the caller already received —
// submitRun, jobStatus and the SSE terminal write all apply their result to
// jobStore first and only afterwards give this persister a non-blocking
// nudge (poke) that it may ignore if it is already about to look.
//
// A job whose control-plane write fails ambiguously is retried by asking the
// control plane about the very same job id and route binding again — never
// by resubmitting to the backend or minting a new job id. Those are two
// separate failure domains (backend submission, persistence write), and
// STATUS.md's J05 requires they stay that way: an ambiguous persistence
// result is never grounds for generating a second, unwanted ComfyUI run.
//
// This persister is a best-effort side channel, not a durable queue: its
// retry state lives in jobStore's own bookkeeping (job.needsPersist,
// job.nextPersistAt), which is exactly as durable as the rest of the
// in-memory job table — gone on a process restart, and capped by the same
// DefaultMaxJobs eviction. What it does guarantee is that nothing is retried
// forever at an unbounded rate: dueForPersist bounds every tick's batch, a
// semaphore bounds concurrency, and per-job backoff spaces out repeated
// failures. It does not guarantee delivery — see the ControlPlane README's
// 已知缺口 for what a job evicted before it is ever persisted means, and
// STATUS.md's J06 for what a Gateway restart with unpersisted jobs still in
// memory means.
//
// jobPersister 是本 Gateway 副本内存 job 表到控制面 jobs 表的旁路写入路径，
// 对应 STATUS.md 的 J05 与 ControlPlane README「Job 持久化契约」。它绝不会
// 挡在一次提交或一次状态观测与调用方已经收到的响应之间——submitRun、jobStatus
// 与 SSE 的终态写入都是先把结果应用到 jobStore，之后才给这个持久化器一次不阻塞
// 的提醒（poke），它若已经打算查看，可以忽略这次提醒。
//
// 一个控制面写入结果不明的 job，重试方式是再次就同一个 job id 与路由绑定询问
// 控制面——绝不重新提交给后端，也绝不铸造一个新的 job id。这是两个独立的故障
// 域（后端提交、持久化写入），STATUS.md 的 J05 要求它们保持独立：一次含糊的
// 持久化结果，绝不能成为生成第二次没人要的 ComfyUI 运行的理由。
//
// 这个持久化器是尽力而为的旁路，不是可靠队列：它的重试状态存放在 jobStore 自己
// 的记账里（job.needsPersist、job.nextPersistAt），与内存 job 表其余部分同样
// 「可靠」——进程重启即丢，且受同一个 DefaultMaxJobs 逐出上限约束。它保证的是
// 不会有什么东西以无界的频率被无限重试：dueForPersist 限定每一轮的批次，一个
// 信号量限定并发，逐 job 的退避拉开重复失败之间的间隔。它不保证送达——一个还没
// 来得及持久化就被逐出的 job 意味着什么，见 ControlPlane README 的「已知缺口」；
// 一次仍有未持久化 job 留在内存里的 Gateway 重启意味着什么，见 STATUS.md 的 J06。
type jobPersister struct {
	jobs   *jobStore
	client JobPersistClient
	clock  runtime.Clock
	logger *slog.Logger
	cfg    jobPersistConfig
	// opener and storage are both nil-able and independent of client: opener
	// is nil only in tests that do not exercise artifact byte persistence,
	// and storage is nil whenever the deployment has no ArtifactStorage
	// configured (STATUS.md's P04) — in which case persistArtifacts reports
	// artifact metadata exactly as it did before P04 existed.
	//
	// opener 与 storage 都可以为 nil，且相互独立：opener 只在不测试产物字节
	// 持久化的测试里为 nil；storage 在部署未配置 ArtifactStorage
	// （STATUS.md 的 P04）时为 nil——这种情况下 persistArtifacts 上报产物
	// 元数据的方式与 P04 出现之前完全一致。
	opener  artifactOpener
	storage objectstore.Backend

	// poke wakes the loop early after a submit or an observation, so a
	// healthy control plane sees a fresh job persisted within about one
	// round trip rather than waiting out the rest of the current interval.
	// It is buffered at 1 and a send is dropped rather than blocked: a
	// pending wake-up already covers whatever a second one would ask for.
	//
	// poke 在一次提交或一次观测之后提前唤醒循环，好让一个健康的控制面能在
	// 大约一次往返之内看到一个新 job 被持久化，而不必等完当前这一轮剩下的
	// 时间。它的缓冲区大小为 1，发送满了就丢弃而不是阻塞：一次已经在等待处理
	// 的唤醒，已经覆盖了第二次想要的东西。
	poke chan struct{}
	stop chan struct{}
	done chan struct{}
}

// newJobPersister builds a persister with cfg's zero fields replaced by
// defaults. It does not start the background loop; call run in a goroutine
// for that.
//
// newJobPersister 用默认值填补 cfg 里的零值字段来构建一个持久化器。它不会
// 启动后台循环，要启动需要以协程方式调用 run。
func newJobPersister(jobs *jobStore, client JobPersistClient, opener artifactOpener, storage objectstore.Backend, clock runtime.Clock, logger *slog.Logger, cfg jobPersistConfig) *jobPersister {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultPersistInterval
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultPersistBatchSize
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultPersistConcurrency
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = DefaultPersistCallTimeout
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = DefaultPersistMaxBackoff
	}
	if cfg.ArtifactCopyTimeout <= 0 {
		cfg.ArtifactCopyTimeout = DefaultArtifactCopyTimeout
	}
	return &jobPersister{
		jobs:    jobs,
		client:  client,
		opener:  opener,
		storage: storage,
		clock:   clock,
		logger:  logger,
		cfg:     cfg,
		poke:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// nudge asks the loop to run a tick sooner than its regular interval. It
// never blocks, and it is nil-receiver-safe: a handlers.persister that is
// nil because no JobPersistClient was configured makes every nudge a no-op,
// so call sites never need their own nil check.
//
// nudge 请求循环提前跑一轮，早于它常规的间隔。它从不阻塞，且对 nil 接收者
// 安全：一个因未配置 JobPersistClient 而为 nil 的 handlers.persister，会让
// 每一次 nudge 都成为空操作，调用点因此从不需要自己做 nil 检查。
func (jp *jobPersister) nudge() {
	if jp == nil {
		return
	}
	select {
	case jp.poke <- struct{}{}:
	default:
	}
}

// run drives the periodic loop until Stop is called. It is meant to be
// started as `go persister.run()`.
//
// run 驱动周期循环，直到 Stop 被调用。它应当以 `go persister.run()` 的方式
// 启动。
func (jp *jobPersister) run() {
	defer close(jp.done)

	ch, stopTimer := jp.clock.NewTimer(jp.cfg.Interval)
	defer stopTimer()
	for {
		select {
		case <-jp.stop:
			return
		default:
		}
		select {
		case <-jp.stop:
			return
		case <-jp.poke:
			jp.tick()
			stopTimer()
			ch, stopTimer = jp.clock.NewTimer(jp.cfg.Interval)
		case <-ch:
			jp.tick()
			ch, stopTimer = jp.clock.NewTimer(jp.cfg.Interval)
		}
	}
}

// tick writes one bounded batch of due jobs and waits for the whole batch to
// finish before returning, so run never arms the next timer while this
// one's calls are still in flight — the same discipline jobSyncer's tick
// follows, for the same reason: a slow batch should delay the next one, not
// pile up behind it.
//
// tick 写入一个有界批次里到期的 job，并等待整批完成后才返回，这样 run 不会在
// 这一轮的调用仍在进行时就安排下一个计时器——与 jobSyncer 的 tick 遵循的是
// 同一条纪律，理由也相同：一轮慢的批次应当推迟下一轮，而不是堆在它后面。
func (jp *jobPersister) tick() {
	now := jp.clock.Now()
	due := jp.jobs.dueForPersist(now, jp.cfg.BatchSize, jp.cfg.Interval)
	dueArtifacts := jp.jobs.dueForArtifactPersist(now, jp.cfg.BatchSize, jp.cfg.Interval)
	if len(due) == 0 && len(dueArtifacts) == 0 {
		return
	}

	sem := make(chan struct{}, jp.cfg.Concurrency)
	var wg sync.WaitGroup
	for _, id := range due {
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			jp.persistOne(id)
		}(id)
	}
	for _, id := range dueArtifacts {
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			jp.persistArtifacts(id)
		}(id)
	}
	wg.Wait()
}

// persistOne brings the control plane's record of one job up to date: a
// CreateJob if it has never confirmed one, then an UpdateJobState if this
// replica has observed something newer than what was last confirmed. Both
// steps read the job fresh from jobStore rather than trusting a snapshot
// taken when the job was claimed, since ObservedSeq may have advanced again
// while an earlier call for the same job was in flight.
//
// persistOne 把控制面对一个 job 的记录追平：如果它从未确认过一次 CreateJob，
// 先做一次；如果本副本观测到的东西比上次确认的更新，再做一次 UpdateJobState。
// 两步都从 jobStore 重新读取该 job，而不是信任认领时的快照，因为在同一个 job
// 更早的一次调用仍在进行时，ObservedSeq 可能已经又前进了。
func (jp *jobPersister) persistOne(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), jp.cfg.CallTimeout)
	defer cancel()

	j, ok := jp.jobs.forPersist(id)
	if !ok {
		return
	}

	if !j.persisted {
		err := jp.client.CreateJob(ctx, j.ID, j.TenantID, j.WorkflowID, j.WorkflowVersion,
			j.Candidate.NodeID, j.Candidate.RuntimeID, j.RunID, string(j.State), 0)
		if err != nil {
			jp.jobs.persistFailed(id, jp.clock.Now(), jp.backoff)
			jp.logger.Warn("job persistence create did not reach the control plane; the run continues, this record is not yet durable",
				slog.String("job_id", id), slog.Any("error", err))
			return
		}
		jp.jobs.persistedCreate(id, jp.clock.Now())

		j, ok = jp.jobs.forPersist(id)
		if !ok {
			return
		}
	}

	if !j.needsPersist() {
		return
	}
	seq := j.ObservedSeq
	_, err := jp.client.UpdateJobState(ctx, j.TenantID, id, string(j.State), j.ErrorSummary, seq)
	if err != nil {
		jp.jobs.persistFailed(id, jp.clock.Now(), jp.backoff)
		jp.logger.Warn("job persistence state update did not reach the control plane",
			slog.String("job_id", id), slog.Any("error", err))
		return
	}
	jp.jobs.persistedState(id, seq, jp.clock.Now())
}

// persistArtifacts reports id's pending artifacts one call per artifact,
// since CreateJobArtifact takes one at a time. When jp.storage is
// configured, each artifact's bytes are first copied from the node that
// produced it into that storage by persistArtifactBytes, and the resulting
// hash, size, content type and storage key ride along on the same
// CreateJobArtifact call; when it is not configured, this reports exactly
// the metadata-only record it always did before P04. A per-artifact failure
// marks the whole job's artifact batch as failed and backs off, but the
// artifacts that did succeed have already been removed from the pending
// list by artifactPersisted, so a retry only ever reports what is still
// owed.
//
// Each artifact gets its own fresh call context rather than sharing one
// across the whole batch, because a byte copy needs jp.cfg.ArtifactCopyTimeout
// — sized for moving real file bytes — not the much smaller
// jp.cfg.CallTimeout a metadata-only call shares this loop with.
//
// persistArtifacts 逐个上报 id 待确认的产物，因为 CreateJobArtifact 一次只
// 接受一个。配置了 jp.storage 时，每个产物的字节会先由 persistArtifactBytes
// 从产出它的节点被复制进那个存储，算出的哈希、大小、内容类型与存储 key
// 随同一次 CreateJobArtifact 调用一起上报；未配置时，本方法上报的仍是 P04
// 出现之前那种仅有元数据的记录。单个产物失败会把整个 job 的这批标记为失败
// 并退避，但已经成功的那些产物已经被 artifactPersisted 从待确认列表移除，
// 因此重试时只会上报依然欠着的部分。
//
// 每个产物都拿到自己独立的一次调用上下文，而不是整批共用一个，因为字节
// 复制需要 jp.cfg.ArtifactCopyTimeout——按搬运真实文件字节设定——而不是与它
// 共处同一个循环、小得多的元数据调用超时 jp.cfg.CallTimeout。
func (jp *jobPersister) persistArtifacts(id string) {
	tenantID, candidate, runID, artifacts, ok := jp.jobs.artifactsForPersist(id)
	if !ok {
		return
	}

	failed := false
	for _, a := range artifacts {
		sha256Hex, contentType, storageKey := "", "", ""
		sizeBytes := int64(0)
		if jp.storage != nil {
			var err error
			sha256Hex, contentType, storageKey, sizeBytes, err = jp.persistArtifactBytes(candidate, runID, tenantID, id, a)
			if err != nil {
				failed = true
				jp.logger.Warn("copying an artifact into object storage failed; it remains downloadable live from the node, this copy is not yet durable",
					slog.String("job_id", id), slog.String("artifact_id", a.ArtifactID), slog.Any("error", err))
				continue
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), jp.cfg.CallTimeout)
		err := jp.client.CreateJobArtifact(ctx, id, a.ArtifactID, tenantID, a.Filename, a.Subfolder, a.Type, sha256Hex, contentType, storageKey, sizeBytes)
		cancel()
		if err != nil {
			failed = true
			jp.logger.Warn("job artifact persistence did not reach the control plane; the artifact remains downloadable, this record is not yet durable",
				slog.String("job_id", id), slog.String("artifact_id", a.ArtifactID), slog.Any("error", err))
			continue
		}
		if storageKey != "" {
			jp.jobs.artifactStored(a.ArtifactID, storageKey, contentType, sizeBytes)
		}
		jp.jobs.artifactPersisted(id, a.ArtifactID)
	}
	if failed {
		jp.jobs.artifactPersistFailed(id, jp.clock.Now(), jp.backoff)
	}
}

// persistArtifactBytes pulls one artifact's bytes from the node that
// produced it (via jp.opener, the same OpenArtifact a live download uses)
// and copies them into jp.storage under a key derived from tenantID, jobID
// and the artifact's own public id — three already-safe, internally
// generated identifiers, so the key never carries anything a caller
// supplied. It hashes and counts the bytes as they are copied, through
// countingReader wrapping the same read Put already does, rather than
// reading the artifact a second time.
//
// persistArtifactBytes 从产出某个产物的节点拉取它的字节（经由 jp.opener，
// 与一次实时下载所用的 OpenArtifact 相同），并复制进 jp.storage，key 由
// tenantID、jobID 与产物自己的公开 id 派生——三者都已经是安全的、内部生成的
// 标识符，因此这个 key 里从不会带上调用方提供的任何东西。它借助包裹在 Put
// 本就要做的那次读取之外的 countingReader，在复制字节的同时完成哈希与计数，
// 而不必把产物再读一遍。
func (jp *jobPersister) persistArtifactBytes(candidate scheduler.Candidate, runID, tenantID, jobID string, a pendingArtifact) (sha256Hex, contentType, storageKey string, size int64, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), jp.cfg.ArtifactCopyTimeout)
	defer cancel()

	ref := runtime.ArtifactRef{RunID: runID, Filename: a.Filename, Subfolder: a.Subfolder, Type: a.Type}
	artifact, err := jp.opener.OpenArtifact(ctx, candidate, ref)
	if err != nil {
		return "", "", "", 0, err
	}
	defer artifact.Body.Close()

	// path.Join rather than string concatenation, so an empty tenantID —
	// what an unconfigured, no-auth deployment's requests carry (see
	// auth.go's authenticator) — collapses cleanly instead of producing a
	// leading "/" that objectstore.validateKey would then reject as an
	// absolute path.
	//
	// 用 path.Join 而不是字符串拼接，这样一个空 tenantID——未配置鉴权、放行
	// 一切的部署（见 auth.go 的 authenticator）的请求所携带的值——会被干净地
	// 折叠掉，而不会产生一个开头的 "/"，进而被 objectstore.validateKey 当作
	// 绝对路径拒绝。
	key := path.Join(tenantID, jobID, a.ArtifactID)
	hash := sha256.New()
	counted := &countingReader{r: io.TeeReader(artifact.Body, hash)}
	if err := jp.storage.Put(ctx, key, counted, artifact.Size); err != nil {
		return "", "", "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), artifact.ContentType, key, counted.n, nil
}

// countingReader wraps a Reader to count the bytes actually read through
// it, so a caller mid-stream (like persistArtifactBytes, hashing the same
// bytes objectstore.Backend.Put is reading) learns the real byte count
// without a second, separate read of whatever it wrapped.
//
// countingReader 包装一个 Reader 以统计实际经它读取的字节数，好让调用方
// （比如 persistArtifactBytes，正在为 objectstore.Backend.Put 读的同一批
// 字节计算哈希）不必对被包装的内容再单独读一遍就能得到真实字节数。
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// backoff doubles the base interval per consecutive failure, capped at
// MaxBackoff — the same shape as jobSyncer's own backoff, applied to a
// different failure domain (the control plane, not a node).
//
// backoff 按连续失败次数把基础间隔逐次翻倍，上限为 MaxBackoff——与 jobSyncer
// 自己的 backoff 形状相同，只是施加于一个不同的故障域（控制面，而不是节点）。
func (jp *jobPersister) backoff(failures int) time.Duration {
	d := jp.cfg.Interval
	for i := 1; i < failures; i++ {
		d *= 2
		if d >= jp.cfg.MaxBackoff {
			return jp.cfg.MaxBackoff
		}
	}
	return d
}

// Stop ends the background loop and waits for the current tick, if any, to
// finish — bounded by batch size, concurrency and per-call timeout, so this
// wait is itself bounded rather than an open-ended drain.
//
// Stop 结束后台循环，并等待正在进行的一轮（如果有）跑完——受批次大小、并发度
// 与单次调用超时约束，因此这个等待本身是有界的，而不是一次无限期的排空。
func (jp *jobPersister) Stop() {
	close(jp.stop)
	<-jp.done
}
