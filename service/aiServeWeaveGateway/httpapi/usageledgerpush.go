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
// (AGENTS.md's "任何一跳都不得无界缓冲"): records awaiting retry are capped at
// BufferSize of their own, beside the channel's, and the oldest batches are
// dropped, counted, once it is exceeded. A batch the control plane refuses
// (ErrUsageBatchRejected) is dropped at once, and one that keeps failing is
// dropped after MaxUsageLedgerBatchAttempts, so no single batch can hold the
// ones behind it forever.
//
// usageledgerpush.go 是 Gateway 一侧对 STATUS.md P2 用量账本有界、异步的
// 那一半：它缓冲 recordUsage（ratelimit.go）入队的 UsageRecord，按定时器或
// 攒满一批（以先到者为准）批量推送给控制面。它从不阻塞刚刚应答完一次请求的
// handler——这里的一次数据库故障不能变成推理路径的延迟。
//
// 与 requestlogpush.go 不同，它会保留失败的批次并按退避重试，因为控制面依据这份
// 账本从预付余额中扣费，丢掉一批就是一批没人付钱的用量。重试是安全的，因为控制面按
// 请求 id 去重；重试也是有界的（AGENTS.md 的"任何一跳都不得无界缓冲"）：待重试的
// 记录在 channel 之外另有一条 BufferSize 上界，超出时丢弃最旧的批次并计数。被控制面
// 拒绝的批次（ErrUsageBatchRejected）立即丢弃，持续失败的批次在
// MaxUsageLedgerBatchAttempts 次后丢弃，任何一个批次都不能永远挡住后面的批次。
package httpapi

import (
	"context"
	"errors"
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

// MaxUsageLedgerBatchAttempts is how many times one batch is pushed before it
// is dropped. With the backoff doubling from FlushInterval up to a minute,
// the default settings give a batch about a quarter of an hour, enough to
// ride out a control plane restart, while a batch that fails for its own
// reasons stops holding up the rest.
//
// MaxUsageLedgerBatchAttempts 是一个批次被丢弃前的推送次数。退避从 FlushInterval
// 起翻倍直至一分钟，默认设置下一个批次约有一刻钟，足以熬过一次控制面重启，而因自身
// 原因失败的批次也不会再挡住其余批次。
const MaxUsageLedgerBatchAttempts = 20

// ErrUsageBatchRejected marks a push the control plane refused for its
// content (400, 413 or 422). Retrying the same batch cannot succeed, so it is
// dropped. An authentication failure is not one of these: it is a
// configuration fault that a fix on either side cures, so it is retried.
//
// ErrUsageBatchRejected 标记控制面因内容而拒绝的推送（400、413 或 422）。重试同一
// 批次不可能成功，因此将其丢弃。鉴权失败不在此列：它是配置故障，任一侧修正即可恢复，
// 因此会被重试。
var ErrUsageBatchRejected = errors.New("httpapi: the control plane rejected the usage batch")

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
		headAttempts int // failed pushes of pending[0] / pending[0] 的失败推送次数
		backoff      time.Duration
		retryAt      time.Time
	)
	// drop discards records, counting each one as dropped.
	//
	// drop 丢弃 records，每条都计为一次丢弃。
	drop := func(records []UsageRecord, reason string) {
		p.logger.Error("dropping a usage batch", slog.String("reason", reason), slog.Int("batch_size", len(records)))
		for range records {
			p.metrics.UsageLedgerDropped()
		}
	}
	// push sends records once. ok reports success; rejected reports a refusal
	// that no retry can change.
	//
	// push 发送一次 records。ok 报告是否成功；rejected 报告一次任何重试都改变不了的拒绝。
	push := func(records []UsageRecord) (ok, rejected bool) {
		ctx, cancel := context.WithTimeout(context.Background(), p.cfg.CallTimeout)
		defer cancel()
		if err := p.client.PushUsageRecords(ctx, records); err != nil {
			p.logger.Warn("pushing usage records to the control plane failed",
				slog.Any("error", err), slog.Int("batch_size", len(records)))
			p.metrics.UsageLedgerPushFailed()
			return false, errors.Is(err, ErrUsageBatchRejected)
		}
		return true, false
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
			drop(pending[0], "the retry backlog exceeds the buffer size")
			pendingCount -= len(pending[0])
			pending = pending[1:]
			headAttempts = 0
		}
	}
	// pop removes the oldest held batch. pop 移除最旧的待重试批次。
	pop := func() {
		pendingCount -= len(pending[0])
		pending = pending[1:]
		headAttempts = 0
	}
	// retry pushes held batches oldest first, stopping at the first failure
	// and doubling the backoff; a rejected batch, or one out of attempts, is
	// dropped and the next one tried. It reports whether nothing is held
	// anymore.
	//
	// retry 从最旧的开始推送待重试批次，遇到第一次失败即停止并把退避加倍；被拒绝或
	// 用尽次数的批次被丢弃，然后尝试下一个。返回值报告是否已不再有待重试的批次。
	retry := func(force bool) bool {
		if len(pending) == 0 {
			return true
		}
		if !force && p.clock.Now().Before(retryAt) {
			return false
		}
		for len(pending) > 0 {
			ok, rejected := push(pending[0])
			switch {
			case ok:
				pop()
				continue
			case rejected:
				drop(pending[0], "the control plane rejected it")
				pop()
				continue
			}
			if headAttempts++; headAttempts >= MaxUsageLedgerBatchAttempts {
				drop(pending[0], "it failed every attempt")
				pop()
				continue
			}
			backoff = min(max(backoff*2, p.cfg.FlushInterval), MaxUsageLedgerRetryBackoff)
			retryAt = p.clock.Now().Add(backoff)
			return false
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
		switch {
		case !drained:
			hold(batch)
		default:
			ok, rejected := push(batch)
			switch {
			case ok:
			case rejected:
				drop(batch, "the control plane rejected it")
			default:
				hold(batch)
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
