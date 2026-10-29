// usageledgerpush.go is the Gateway's bounded, asynchronous side of
// STATUS.md's P2 usage ledger: it buffers UsageRecord values enqueued by
// recordUsage (ratelimit.go) and pushes them to the control plane in
// batches, on a timer or once a batch fills, whichever comes first. It
// never blocks the handler that just answered a request — a database hiccup
// here must not become inference-path latency.
//
// Unlike requestlogpush.go it keeps a failed batch and retries it with
// backoff, because the control plane charges prepaid balances from this
// ledger and a dropped batch is usage nobody pays for. The retry is safe
// because the control plane deduplicates on request id, and it stays bounded
// (AGENTS.md's "任何一跳都不得无界缓冲"): batches awaiting retry share
// BufferSize, and the oldest are dropped, counted, once it is exceeded.
//
// usageledgerpush.go 是 Gateway 一侧对 STATUS.md P2 用量账本有界、异步的
// 那一半：它缓冲 recordUsage（ratelimit.go）入队的 UsageRecord，按定时器或
// 攒满一批（以先到者为准）批量推送给控制面。它从不阻塞刚刚应答完一次请求的
// handler——这里的一次数据库故障不能变成推理路径的延迟。
//
// 与 requestlogpush.go 不同，它会保留失败的批次并按退避重试，因为控制面依据这份
// 账本从预付余额中扣费，丢掉一批就是一批没人付钱的用量。重试是安全的，因为控制面按
// 请求 id 去重；重试也是有界的（AGENTS.md 的"任何一跳都不得无界缓冲"）：待重试的
// 批次共用 BufferSize，超出时丢弃最旧的批次并计数。
package httpapi

import (
	"context"
	"log/slog"
	"time"

	"AIServeWeave/common/runtime"
)

// UsageLedgerClient pushes a batch of usage records to the control plane. It
// is declared here, where it is used, and implemented in controlplaneclient
// — the same split RequestLogClient already uses, so this package stays
// testable without a control plane.
//
// UsageLedgerClient 把一批用量记录推送给控制面。它声明在使用它的这里，实现在
// controlplaneclient——与 RequestLogClient 已经采用的是同一种拆分，好让本
// 包无需控制面即可测试。
type UsageLedgerClient interface {
	PushUsageRecords(ctx context.Context, records []UsageRecord) error
}

// usageLedgerPushConfig tunes the pusher. Zero fields take the package
// defaults below.
//
// usageLedgerPushConfig 调整推送器的参数。零值字段采用下面的包默认值。
type usageLedgerPushConfig struct {
	BufferSize    int
	BatchSize     int
	FlushInterval time.Duration
	CallTimeout   time.Duration
}

// Package defaults for usageLedgerPushConfig, matching requestlogpush.go's
// own defaults exactly: this buffer is bounded for the same reason, and
// there is no evidence yet that usage records need a different batching
// cadence than request logs.
//
// usageLedgerPushConfig 的包默认值，与 requestlogpush.go 自己的默认值完全
// 一致：这个缓冲有界的理由相同，且目前没有证据表明用量记录需要与请求日志
// 不同的攒批节奏。
const (
	DefaultUsageLedgerBufferSize    = 10000
	DefaultUsageLedgerBatchSize     = 500
	DefaultUsageLedgerFlushInterval = 5 * time.Second
	DefaultUsageLedgerCallTimeout   = 3 * time.Second
)

// usageLedgerPusher implements usageLedgerSink.
//
// usageLedgerPusher 实现 usageLedgerSink。
type usageLedgerPusher struct {
	client  UsageLedgerClient
	clock   runtime.Clock
	logger  *slog.Logger
	metrics *recorder
	cfg     usageLedgerPushConfig

	ch   chan UsageRecord
	stop chan struct{}
	done chan struct{}
}

// newUsageLedgerPusher builds a pusher with cfg's zero fields replaced by
// defaults. It does not start the background loop; call run in a goroutine
// for that — the same two-step construction newRequestLogPusher already
// uses.
//
// newUsageLedgerPusher 用默认值填补 cfg 里的零值字段来构建一个推送器。它不会
// 启动后台循环，要启动需要以协程方式调用 run——与 newRequestLogPusher 相同的
// 两步构造。
func newUsageLedgerPusher(client UsageLedgerClient, clock runtime.Clock, logger *slog.Logger, metrics *recorder, cfg usageLedgerPushConfig) *usageLedgerPusher {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = DefaultUsageLedgerBufferSize
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultUsageLedgerBatchSize
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = DefaultUsageLedgerFlushInterval
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = DefaultUsageLedgerCallTimeout
	}
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if metrics == nil {
		metrics = newRecorder(nil)
	}
	return &usageLedgerPusher{
		client: client, clock: clock, logger: logger, metrics: metrics, cfg: cfg,
		ch:   make(chan UsageRecord, cfg.BufferSize),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
}

// enqueue implements usageLedgerSink. It never blocks: a full buffer drops
// the new record and reports false, which the caller (recordUsage) turns
// into a metric — blocking here would put the push on the request path.
//
// enqueue 实现 usageLedgerSink。它从不阻塞：缓冲已满时丢弃新记录并报告
// false，调用方（recordUsage）把它变成一次指标——在这里阻塞会把推送放到请求
// 路径上。
func (p *usageLedgerPusher) enqueue(r UsageRecord) bool {
	select {
	case p.ch <- r:
		return true
	default:
		return false
	}
}

// MaxUsageLedgerRetryBackoff caps the wait between retries of a failed push.
//
// MaxUsageLedgerRetryBackoff 是失败推送两次重试之间等待时间的上限。
const MaxUsageLedgerRetryBackoff = time.Minute

// run drains the buffer, flushing a batch to the control plane either when
// BatchSize records have accumulated or FlushInterval has elapsed since the
// last flush, whichever comes first. A failed batch waits for its retry, and
// while any batch is waiting new batches queue behind it rather than being
// pushed past it, so a control plane that is down is not hammered. It is
// meant to be started as `go pusher.run()`.
//
// run 消费缓冲，在攒够 BatchSize 条记录或距上次 flush 已过 FlushInterval——
// 以先到者为准——时向控制面 flush 一批。失败的批次等待重试；只要还有批次在等待，
// 新批次就排在它后面而不是越过它推送，因此一个宕机的控制面不会被反复冲击。它应当
// 以 `go pusher.run()` 的方式启动。
func (p *usageLedgerPusher) run() {
	defer close(p.done)
	ticker, stopTicker := p.clock.NewTimer(p.cfg.FlushInterval)
	defer stopTicker()

	var (
		pending      [][]UsageRecord
		pendingCount int
		backoff      time.Duration
		retryAt      time.Time
	)
	push := func(records []UsageRecord) bool {
		ctx, cancel := context.WithTimeout(context.Background(), p.cfg.CallTimeout)
		defer cancel()
		if err := p.client.PushUsageRecords(ctx, records); err != nil {
			p.logger.Warn("pushing usage records to the control plane failed; the batch is kept for retry",
				slog.Any("error", err), slog.Int("batch_size", len(records)))
			p.metrics.UsageLedgerPushFailed()
			return false
		}
		return true
	}
	// hold queues records for retry, dropping the oldest held batches once
	// the held total would exceed BufferSize.
	//
	// hold 把 records 排入重试队列；一旦待重试总量会超过 BufferSize，就丢弃最旧的
	// 待重试批次。
	hold := func(records []UsageRecord) {
		pending = append(pending, records)
		pendingCount += len(records)
		for pendingCount > p.cfg.BufferSize && len(pending) > 1 {
			for range pending[0] {
				p.metrics.UsageLedgerDropped()
			}
			pendingCount -= len(pending[0])
			pending = pending[1:]
		}
	}
	// retry pushes held batches oldest first, stopping at the first failure
	// and doubling the backoff; it reports whether nothing is held anymore.
	//
	// retry 从最旧的开始推送待重试批次，遇到第一次失败即停止并把退避加倍；返回值
	// 报告是否已不再有待重试的批次。
	retry := func(force bool) bool {
		if len(pending) == 0 {
			return true
		}
		if !force && p.clock.Now().Before(retryAt) {
			return false
		}
		for len(pending) > 0 {
			if !push(pending[0]) {
				backoff = min(max(backoff*2, p.cfg.FlushInterval), MaxUsageLedgerRetryBackoff)
				retryAt = p.clock.Now().Add(backoff)
				return false
			}
			pendingCount -= len(pending[0])
			pending = pending[1:]
		}
		backoff = 0
		return true
	}

	batch := make([]UsageRecord, 0, p.cfg.BatchSize)
	flush := func(force bool) {
		drained := retry(force)
		if len(batch) == 0 {
			return
		}
		if !drained || !push(batch) {
			hold(batch)
			if drained {
				backoff = min(max(backoff*2, p.cfg.FlushInterval), MaxUsageLedgerRetryBackoff)
				retryAt = p.clock.Now().Add(backoff)
			}
		}
		batch = make([]UsageRecord, 0, p.cfg.BatchSize)
	}

	for {
		select {
		case <-p.stop:
			flush(true)
			return
		case r := <-p.ch:
			batch = append(batch, r)
			if len(batch) >= p.cfg.BatchSize {
				flush(false)
			}
		case <-ticker:
			flush(false)
			ticker, stopTicker = p.clock.NewTimer(p.cfg.FlushInterval)
		}
	}
}

// Stop signals run to flush whatever it currently holds and exit, and
// waits for it to do so.
//
// Stop 通知 run flush 掉当前持有的内容并退出，并等待其完成。
func (p *usageLedgerPusher) Stop() {
	close(p.stop)
	<-p.done
}
