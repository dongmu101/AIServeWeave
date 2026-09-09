package httpapi

import (
	"context"
	"log/slog"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/objectstore"
)

// Defaults for the background artifact cleanup sweeper (STATUS.md's P04).
// DefaultArtifactRetention and DefaultArtifactPreviewRetention are the two
// halves of "预览图单独处理": a final "output" artifact is worth keeping far
// longer than a "temp" one — a ComfyUI preview/intermediate frame, usually
// numerous and rarely what a caller wants to keep once the run has finished.
//
// 后台产物清理扫描器（STATUS.md 的 P04）的默认值。DefaultArtifactRetention
// 与 DefaultArtifactPreviewRetention 是「预览图单独处理」的两半：一个最终的
// "output" 产物值得比一个 "temp" 产物保留久得多——后者是 ComfyUI 的
// 预览/中间帧，数量通常不少，运行结束后也很少是调用方想留着的东西。
const (
	DefaultArtifactCleanupInterval    = 10 * time.Minute
	DefaultArtifactRetention          = 30 * 24 * time.Hour
	DefaultArtifactPreviewRetention   = 24 * time.Hour
	DefaultArtifactCleanupCallTimeout = 10 * time.Second
)

// ArtifactCleanupClient is what the background cleanup sweeper needs from
// the control plane's internal Job API (STATUS.md's P04).
//
// It is declared here with primitive-typed parameters rather than by
// importing controlplaneclient's own richer types, for the same reason
// JobPersistClient is: controlplaneclient already imports this package (for
// Identity and KeyVerifier), so the reverse import would be a cycle.
// controlplaneclient provides an adapter satisfying this interface; see its
// GatewayPersister.
//
// ArtifactCleanupClient 是后台清理扫描器需要从控制面内部 Job API
// （STATUS.md 的 P04）取得的东西。
//
// 这里用原始类型参数声明它，而不是导入 controlplaneclient 自己更丰富的
// 类型，理由与 JobPersistClient 相同：controlplaneclient 已经导入了本包
// （用于 Identity 与 KeyVerifier），反过来导入就会成环。controlplaneclient
// 提供一个满足本接口的适配器，见它的 GatewayPersister。
type ArtifactCleanupClient interface {
	// ListExpiredJobArtifacts returns artifacts of artifactType created
	// before cutoff, across every tenant — see the control plane's own
	// ListJobArtifactsBefore for why this is not tenant-scoped.
	//
	// ListExpiredJobArtifacts 返回类型为 artifactType、创建于 cutoff 之前的
	// 产物，跨越所有租户——为什么这不按租户限定范围，见控制面自己的
	// ListJobArtifactsBefore。
	ListExpiredJobArtifacts(ctx context.Context, artifactType string, cutoff time.Time) ([]ExpiredJobArtifact, error)
	// DeleteJobArtifact removes one artifact record. Like CreateJobArtifact,
	// deleting an id already gone is not an error.
	//
	// DeleteJobArtifact 移除一个产物记录。与 CreateJobArtifact 一样，删除一个
	// 已经不在的 id 不算错误。
	DeleteJobArtifact(ctx context.Context, jobID, artifactID string) error
}

// ExpiredJobArtifact is one artifact record the cleanup sweeper may reap.
// Unlike the metadata jobPersister reports, this carries StorageKey: the
// sweeper is exactly the party that needs it, to delete the object storage
// bytes before the row itself.
//
// ExpiredJobArtifact 是清理扫描器可能回收的一条产物记录。与 jobPersister
// 上报的元数据不同，这里携带 StorageKey：扫描器正是需要它的那一方，用来在
// 删除这一行本身之前，先删掉对象存储里的字节。
type ExpiredJobArtifact struct {
	ArtifactID string
	JobID      string
	TenantID   string
	Type       string
	StorageKey string
	CreatedAt  time.Time
}

// artifactCleanupConfig collects the sweeper's tunable bounds, defaulted by
// newArtifactCleaner so callers only need to set what they want to
// override.
//
// artifactCleanupConfig 收集扫描器的可调上限，由 newArtifactCleaner 补上
// 默认值，因此调用方只需设置想要覆盖的部分。
type artifactCleanupConfig struct {
	Interval        time.Duration
	CallTimeout     time.Duration
	RetentionByType map[string]time.Duration
}

// artifactCleaner is STATUS.md's P04 retention sweep: it periodically asks
// the control plane which artifacts, per type, are older than that type's
// configured retention, deletes each one's bytes from object storage (when
// it was ever persisted there), and only then deletes the metadata row —
// never the other order, which would orphan bytes with no record pointing
// at them for any future sweep to find.
//
// It shares dueForPersist's "bounded, best-effort, not a durable queue"
// nature in spirit but not in mechanism: there is no in-memory retry table
// and no exponential backoff here, because there is nothing to retry
// against — an artifact still past its retention on the next tick is found
// again by the same ListExpiredJobArtifacts call that found it this time,
// so a failed delete this tick is simply retried at the next fixed
// interval. That is coarser than jobPersister's per-job backoff, and
// deliberately so: a cleanup sweep has no caller waiting on it, so a
// repeatedly failing control plane costs this sweeper nothing sharper than
// one more empty tick.
//
// artifactCleaner 是 STATUS.md P04 的保留期清理扫描：它周期性地向控制面
// 询问每个类型里哪些产物早于该类型配置的保留期，先删掉每一个的对象存储
// 字节（如果它当初确曾被持久化到那里），然后才删除元数据行——顺序绝不能
// 反过来，否则会留下一堆字节孤儿，没有任何记录指向它们，未来的扫描也就
// 无从找到它们。
//
// 它在精神上与 dueForPersist「有界、尽力而为、不是可靠队列」的性质相同，
// 但机制不同：这里既没有内存重试表，也没有指数退避，因为没有什么可供
// 重试——一个下一轮仍然超过保留期的产物，会被同一个 ListExpiredJobArtifacts
// 调用再次找到，因此这一轮失败的删除，下一个固定间隔简单地重试即可。这比
// jobPersister 逐 job 的退避更粗糙，且是刻意的：一次清理扫描没有调用方在等
// 它，一个反复失败的控制面，给这个扫描器带来的代价不过是又一轮空转。
type artifactCleaner struct {
	client  ArtifactCleanupClient
	storage objectstore.Backend
	clock   runtime.Clock
	logger  *slog.Logger
	cfg     artifactCleanupConfig

	stop chan struct{}
	done chan struct{}
}

// newArtifactCleaner builds a sweeper with cfg's zero fields replaced by
// defaults. It does not start the background loop; call run in a goroutine
// for that.
//
// newArtifactCleaner 用默认值填补 cfg 里的零值字段来构建一个扫描器。它不会
// 启动后台循环，要启动需要以协程方式调用 run。
func newArtifactCleaner(client ArtifactCleanupClient, storage objectstore.Backend, clock runtime.Clock, logger *slog.Logger, cfg artifactCleanupConfig) *artifactCleaner {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultArtifactCleanupInterval
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = DefaultArtifactCleanupCallTimeout
	}
	if cfg.RetentionByType == nil {
		cfg.RetentionByType = map[string]time.Duration{
			"output": DefaultArtifactRetention,
			"temp":   DefaultArtifactPreviewRetention,
		}
	}
	return &artifactCleaner{
		client:  client,
		storage: storage,
		clock:   clock,
		logger:  logger,
		cfg:     cfg,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// run drives the periodic loop until Stop is called. It is meant to be
// started as `go cleaner.run()`.
//
// run 驱动周期循环，直到 Stop 被调用。它应当以 `go cleaner.run()` 的方式
// 启动。
func (c *artifactCleaner) run() {
	defer close(c.done)

	ch, stopTimer := c.clock.NewTimer(c.cfg.Interval)
	defer stopTimer()
	for {
		select {
		case <-c.stop:
			return
		case <-ch:
			c.tick()
			ch, stopTimer = c.clock.NewTimer(c.cfg.Interval)
		}
	}
}

// tick sweeps every configured (type, retention) pair once. Unlike
// jobPersister's tick this has no batch/concurrency limit to apply beyond
// what the control plane's own ListJobArtifactsBefore already bounds
// itself to (store.MaxExpiredJobArtifacts) — a cleanup sweep has no caller
// waiting on it the way a request does, so there is no latency budget this
// needs to respect beyond finishing before the next tick is due.
//
// tick 把每一对已配置的 (类型, 保留期) 都扫一遍。与 jobPersister 的 tick
// 不同，这里没有需要施加的批次/并发上限——控制面自己的 ListJobArtifactsBefore
// 早已把自己限定在 store.MaxExpiredJobArtifacts 之内——一次清理扫描不像一次
// 请求那样有调用方在等它，因此除了要在下一轮到期之前跑完之外，没有别的
// 延迟预算需要照顾。
func (c *artifactCleaner) tick() {
	for artifactType, retention := range c.cfg.RetentionByType {
		c.sweepType(artifactType, retention)
	}
}

func (c *artifactCleaner) sweepType(artifactType string, retention time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.CallTimeout)
	defer cancel()

	cutoff := c.clock.Now().Add(-retention)
	expired, err := c.client.ListExpiredJobArtifacts(ctx, artifactType, cutoff)
	if err != nil {
		c.logger.Warn("listing expired artifacts failed; they remain in place, this sweep contributes nothing this tick",
			slog.String("type", artifactType), slog.Any("error", err))
		return
	}

	for _, a := range expired {
		c.reap(a)
	}
}

// reap deletes one artifact's object storage bytes (if it ever had any) and
// then its metadata row, in that order — see the type doc comment for why
// the order is load-bearing.
//
// reap 删除一个产物的对象存储字节（如果它当初确曾拥有）以及随后的元数据行——
// 顺序为何要紧，见类型的文档注释。
func (c *artifactCleaner) reap(a ExpiredJobArtifact) {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.CallTimeout)
	defer cancel()

	if a.StorageKey != "" && c.storage != nil {
		if err := c.storage.Delete(ctx, a.StorageKey); err != nil {
			c.logger.Warn("deleting an expired artifact's bytes from object storage failed; its metadata row is left in place so a future sweep finds it again",
				slog.String("artifact_id", a.ArtifactID), slog.String("storage_key", a.StorageKey), slog.Any("error", err))
			return
		}
	}
	if err := c.client.DeleteJobArtifact(ctx, a.JobID, a.ArtifactID); err != nil {
		c.logger.Warn("deleting an expired artifact's metadata row failed; its bytes are already gone, a future sweep will retry the row",
			slog.String("artifact_id", a.ArtifactID), slog.Any("error", err))
	}
}

// Stop ends the background loop and waits for the current tick, if any, to
// finish.
//
// Stop 结束后台循环，并等待正在进行的一轮（如果有）跑完。
func (c *artifactCleaner) Stop() {
	close(c.stop)
	<-c.done
}
