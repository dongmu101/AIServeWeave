// requestlogpush.go is the Gateway's bounded, asynchronous side of
// STATUS.md's P09/C28: it buffers requestLogRecord values enqueued by
// requestlog.go's middleware and pushes them to the control plane in
// batches, on a timer or once a batch fills, whichever comes first. It
// never blocks the middleware and never retries a failed push — both are
// deliberate per the design doc: these are diagnostic records, not job
// state, and a database hiccup here must not become inference-path latency
// or an unbounded local backlog.
//
// requestlogpush.go 是 Gateway 一侧对 STATUS.md P09/C28 有界、异步的那一半：
// 它缓冲 requestlog.go 中间件入队的 requestLogRecord，按定时器或攒满一批
// (以先到者为准)批量推送给控制面。它从不阻塞中间件，也从不重试失败的推送——
// 两者都是设计文档里刻意的决定：这些是诊断性记录，不是 job 状态，这里的一次
// 数据库故障不能变成推理路径的延迟，也不能变成一份无界的本地积压。
package httpapi

import (
	"context"
	"log/slog"
	"time"

	"AIServeWeave/common/runtime"
)

// RequestLogClient pushes a batch of request-log records to the control
// plane. It is declared here, where it is used, and implemented in
// controlplaneclient — the same split KeyVerifier and JobPersistClient
// already use, so this package stays testable without a control plane.
//
// RequestLogClient 把一批请求记录推送给控制面。它声明在使用它的这里，实现在
// controlplaneclient——与 KeyVerifier、JobPersistClient 已经采用的是同一种
// 拆分，好让本包无需控制面即可测试。
type RequestLogClient interface {
	PushRequestLogs(ctx context.Context, records []requestLogRecord) error
}

// requestLogPushConfig tunes the pusher. Zero fields take the package
// defaults below.
//
// requestLogPushConfig 调整推送器的参数。零值字段采用下面的包默认值。
type requestLogPushConfig struct {
	BufferSize    int
	BatchSize     int
	FlushInterval time.Duration
	CallTimeout   time.Duration
}

// Package defaults for requestLogPushConfig, chosen to keep the buffer
// bounded (STATUS.md's AGENTS.md security-line "任何一跳都不得无界缓冲")
// while batching enough that a busy replica does not call the control
// plane once per request.
//
// requestLogPushConfig 的包默认值，选定的目标是让缓冲保持有界(AGENTS.md 安全
// 红线"任何一跳都不得无界缓冲")，同时攒够一批，使一个繁忙的副本不至于每个
// 请求都调用一次控制面。
const (
	DefaultRequestLogBufferSize    = 10000
	DefaultRequestLogBatchSize     = 500
	DefaultRequestLogFlushInterval = 5 * time.Second
	DefaultRequestLogCallTimeout   = 3 * time.Second
)

// requestLogPusher implements requestLogSink.
//
// requestLogPusher 实现 requestLogSink。
type requestLogPusher struct {
	client RequestLogClient
	clock  runtime.Clock
	logger *slog.Logger
	cfg    requestLogPushConfig

	ch   chan requestLogRecord
	stop chan struct{}
	done chan struct{}
}

// newRequestLogPusher builds a pusher with cfg's zero fields replaced by
// defaults. It does not start the background loop; call run in a goroutine
// for that — the same two-step construction jobSyncer already uses.
//
// newRequestLogPusher 用默认值填补 cfg 里的零值字段来构建一个推送器。它不会
// 启动后台循环，要启动需要以协程方式调用 run——与 jobSyncer 相同的两步构造。
func newRequestLogPusher(client RequestLogClient, clock runtime.Clock, logger *slog.Logger, cfg requestLogPushConfig) *requestLogPusher {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = DefaultRequestLogBufferSize
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultRequestLogBatchSize
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = DefaultRequestLogFlushInterval
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = DefaultRequestLogCallTimeout
	}
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &requestLogPusher{
		client: client, clock: clock, logger: logger, cfg: cfg,
		ch:   make(chan requestLogRecord, cfg.BufferSize),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
}

// enqueue implements requestLogSink. It never blocks: a full buffer drops
// the new record and reports false, which the caller (requestlog.go's
// middleware) turns into a metric rather than a retry — see this file's
// package doc comment for why blocking or retrying here is exactly what
// must not happen.
//
// enqueue 实现 requestLogSink。它从不阻塞：缓冲已满时丢弃新记录并报告
// false，调用方(requestlog.go 的中间件)把它变成一次指标而不是一次重试——为
// 什么在这里阻塞或重试正是不该发生的事，见本文件的包文档注释。
func (p *requestLogPusher) enqueue(r requestLogRecord) bool {
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
func (p *requestLogPusher) run() {
	defer close(p.done)
	ticker, stopTicker := p.clock.NewTimer(p.cfg.FlushInterval)
	defer stopTicker()

	batch := make([]requestLogRecord, 0, p.cfg.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), p.cfg.CallTimeout)
		if err := p.client.PushRequestLogs(ctx, batch); err != nil {
			p.logger.Warn("pushing request logs to the control plane failed; the batch is dropped, not retried",
				slog.Any("error", err), slog.Int("batch_size", len(batch)))
		}
		cancel()
		batch = make([]requestLogRecord, 0, p.cfg.BatchSize)
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
func (p *requestLogPusher) Stop() {
	close(p.stop)
	<-p.done
}
