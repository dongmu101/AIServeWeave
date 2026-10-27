// usageledgerpush.go is the Gateway's bounded, asynchronous side of
// STATUS.md's P2 usage ledger: it buffers UsageRecord values enqueued by
// recordUsage (ratelimit.go) and pushes them to the control plane in
// batches, on a timer or once a batch fills, whichever comes first. It
// never blocks the handler that just answered a request and never retries a
// failed push — the same two deliberate choices requestlogpush.go already
// makes, for the same reason: a database hiccup here must not become
// inference-path latency or an unbounded local backlog (AGENTS.md's "任何
// 一跳都不得无界缓冲").
//
// usageledgerpush.go 是 Gateway 一侧对 STATUS.md P2 用量账本有界、异步的
// 那一半：它缓冲 recordUsage（ratelimit.go）入队的 UsageRecord，按定时器或
// 攒满一批（以先到者为准）批量推送给控制面。它从不阻塞刚刚应答完一次请求的
// handler，也从不重试失败的推送——与 requestlogpush.go 已经做出的同两个
// 刻意选择相同，理由也相同：这里的一次数据库故障不能变成推理路径的延迟，
// 也不能变成一份无界的本地积压（AGENTS.md 的"任何一跳都不得无界缓冲"）。
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
// into a metric rather than a retry — see this file's package doc comment
// for why blocking or retrying here is exactly what must not happen.
//
// enqueue 实现 usageLedgerSink。它从不阻塞：缓冲已满时丢弃新记录并报告
// false，调用方（recordUsage）把它变成一次指标而不是一次重试——为什么在
// 这里阻塞或重试正是不该发生的事，见本文件的包文档注释。
func (p *usageLedgerPusher) enqueue(r UsageRecord) bool {
	select {
	case p.ch <- r:
		return true
	default:
		return false
	}
}

// run drains the buffer, flushing a batch to the control plane either when
// BatchSize records have accumulated or FlushInterval has elapsed since the
// last flush, whichever comes first. It is meant to be started as
// `go pusher.run()`.
//
// run 消费缓冲，在攒够 BatchSize 条记录或距上次 flush 已过 FlushInterval——
// 以先到者为准——时向控制面 flush 一批。它应当以 `go pusher.run()` 的方式
// 启动。
func (p *usageLedgerPusher) run() {
	defer close(p.done)
	ticker, stopTicker := p.clock.NewTimer(p.cfg.FlushInterval)
	defer stopTicker()

	batch := make([]UsageRecord, 0, p.cfg.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), p.cfg.CallTimeout)
		if err := p.client.PushUsageRecords(ctx, batch); err != nil {
			p.logger.Warn("pushing usage records to the control plane failed; the batch is dropped, not retried",
				slog.Any("error", err), slog.Int("batch_size", len(batch)))
			p.metrics.UsageLedgerPushFailed()
		}
		cancel()
		batch = make([]UsageRecord, 0, p.cfg.BatchSize)
	}

	for {
		select {
		case <-p.stop:
			flush()
			return
		case r := <-p.ch:
			batch = append(batch, r)
			if len(batch) >= p.cfg.BatchSize {
				flush()
			}
		case <-ticker:
			flush()
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
