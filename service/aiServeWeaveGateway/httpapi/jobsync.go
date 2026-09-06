package httpapi

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

// Defaults for the background job syncer. They bound how much work one
// replica does chasing status on the caller's behalf: at most
// DefaultSyncBatchSize jobs per DefaultSyncInterval, at most
// DefaultSyncConcurrency of them in flight at once, and no single call is
// allowed to hang past DefaultSyncCallTimeout.
//
// 后台 job 同步器的默认值。它们限定了一个副本代调用方追赶状态最多做多少工作：
// 每 DefaultSyncInterval 至多 DefaultSyncBatchSize 个 job，至多 DefaultSyncConcurrency
// 个同时在途，且单次调用不允许挂起超过 DefaultSyncCallTimeout。
const (
	DefaultSyncInterval    = 5 * time.Second
	DefaultSyncBatchSize   = 200
	DefaultSyncConcurrency = 8
	DefaultSyncCallTimeout = 10 * time.Second
	DefaultSyncMaxBackoff  = 5 * time.Minute
)

// workflowStatusAsker is the one scheduler method the syncer needs. It is an
// interface rather than a concrete *scheduler.Scheduler so a test can drive
// the syncer against a stub that fails and recovers on cue, without standing
// up a tunnel.
//
// workflowStatusAsker 是同步器需要的唯一一个 scheduler 方法。它是一个接口而不是
// 具体的 *scheduler.Scheduler，好让测试能针对一个按需失败与恢复的桩来驱动同步器，
// 而无需搭起一整条隧道。
type workflowStatusAsker interface {
	WorkflowStatus(ctx context.Context, c scheduler.Candidate, runID string) (runtime.WorkflowStatus, error)
}

// jobSyncConfig collects the syncer's tunable bounds, defaulted by
// newJobSyncer so callers only need to set what they want to override.
//
// jobSyncConfig 收集同步器的可调上限，由 newJobSyncer 补上默认值，因此调用方只需
// 设置想要覆盖的部分。
type jobSyncConfig struct {
	Interval    time.Duration
	BatchSize   int
	Concurrency int
	CallTimeout time.Duration
	MaxBackoff  time.Duration
}

// jobSyncer periodically asks each non-terminal job's node for its current
// status, so a run advances toward its terminal state even when no caller is
// polling GET /v1/jobs/{job_id} or holding open its SSE event stream —
// README's own "state 是最后观测状态，不是实时状态" only stops being a caveat
// worth stating once something keeps observing on the caller's behalf.
//
// Every tick is bounded on three axes at once: dueForSync caps how many jobs
// are considered, a semaphore caps how many of them are asked concurrently,
// and each individual ask carries its own timeout. A tick runs to completion
// before the next timer is armed, so ticks never overlap and a slow batch
// simply delays the next one rather than piling up.
//
// jobSyncer 周期性地向每个非终态 job 所在的节点询问当前状态，这样即使没有调用方在
// 轮询 GET /v1/jobs/{job_id} 或挂着它的 SSE 事件流，一次运行也能继续走向终态——
// README 自己那句「state 是最后观测状态，不是实时状态」，只有在有什么东西代替调用方
// 持续观测时，才不再是一句需要特别声明的警告。
//
// 每一轮都同时在三个维度上有界：dueForSync 限定考虑多少个 job，一个信号量限定同时
// 询问多少个，每一次单独的询问自带超时。一轮必须跑完才会安排下一个计时器，因此各轮
// 从不重叠，一轮慢的批次只会推迟下一轮，而不会堆积起来。
type jobSyncer struct {
	jobs   *jobStore
	sched  workflowStatusAsker
	clock  runtime.Clock
	logger *slog.Logger
	cfg    jobSyncConfig

	stop chan struct{}
	done chan struct{}
}

// newJobSyncer builds a syncer with cfg's zero fields replaced by defaults.
// It does not start the background loop; call run in a goroutine for that.
//
// newJobSyncer 用默认值填补 cfg 里的零值字段来构建一个同步器。它不会启动后台循环，
// 要启动需要以协程方式调用 run。
func newJobSyncer(jobs *jobStore, sched workflowStatusAsker, clock runtime.Clock, logger *slog.Logger, cfg jobSyncConfig) *jobSyncer {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultSyncInterval
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultSyncBatchSize
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultSyncConcurrency
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = DefaultSyncCallTimeout
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = DefaultSyncMaxBackoff
	}
	return &jobSyncer{
		jobs:   jobs,
		sched:  sched,
		clock:  clock,
		logger: logger,
		cfg:    cfg,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
}

// run drives the periodic loop until Stop is called. It is meant to be
// started as `go syncer.run()`.
//
// run 驱动周期循环，直到 Stop 被调用。它应当以 `go syncer.run()` 的方式启动。
func (js *jobSyncer) run() {
	defer close(js.done)

	ch, stopTimer := js.clock.NewTimer(js.cfg.Interval)
	defer stopTimer()
	for {
		select {
		case <-js.stop:
			return
		default:
		}
		select {
		case <-js.stop:
			return
		case <-ch:
			js.tick()
			ch, stopTimer = js.clock.NewTimer(js.cfg.Interval)
		}
	}
}

// tick asks about one bounded batch of due jobs and waits for the whole
// batch to finish before returning, so run never arms the next timer while
// this one's calls are still in flight.
//
// tick 询问一个有界批次里到期的 job，并等待整批完成后才返回，这样 run 不会在这一轮
// 的调用仍在进行时就安排下一个计时器。
func (js *jobSyncer) tick() {
	now := js.clock.Now()
	due := js.jobs.dueForSync(now, js.cfg.BatchSize, js.cfg.Interval)
	if len(due) == 0 {
		return
	}

	sem := make(chan struct{}, js.cfg.Concurrency)
	var wg sync.WaitGroup
	for _, c := range due {
		wg.Add(1)
		sem <- struct{}{}
		go func(c syncCandidate) {
			defer wg.Done()
			defer func() { <-sem }()
			js.syncOne(c)
		}(c)
	}
	wg.Wait()
}

// syncOne asks one job's node for its status and records the outcome. A
// node that has disappeared answers with a *runtime.RuntimeError — Runtime
// resolves the node fresh on every call and fails cleanly rather than
// blocking when it is gone — so this is an ordinary, expected failure mode
// here, not a reason to log at error level or to give up on the job.
//
// syncOne 向一个 job 的节点询问状态并记录结果。一个已经消失的节点会以
// *runtime.RuntimeError 作答——Runtime 在每次调用时都重新解析节点，节点不在时干净
// 地失败而不是阻塞——因此这在这里是一种普通的、预期内的失败方式，不构成用 error
// 级别记日志或放弃这个 job 的理由。
func (js *jobSyncer) syncOne(c syncCandidate) {
	ctx, cancel := context.WithTimeout(context.Background(), js.cfg.CallTimeout)
	defer cancel()

	status, err := js.sched.WorkflowStatus(ctx, c.Candidate, c.RunID)
	now := js.clock.Now()
	if err != nil {
		js.jobs.syncFailed(c.ID, now, js.backoff)
		js.logger.Warn("background job sync did not reach the node",
			slog.String("job_id", c.ID), slog.String("node_id", c.Candidate.NodeID), slog.Any("error", err))
		return
	}
	js.jobs.syncSucceeded(c.ID, status, now)
}

// backoff doubles the base interval per consecutive failure, capped at
// MaxBackoff, so a node that stays gone is asked about less and less often
// instead of every tick for as long as it remains gone.
//
// backoff 按连续失败次数把基础间隔逐次翻倍，上限为 MaxBackoff，这样一个持续消失的
// 节点会被越来越少地问起，而不是在它消失的整段时间里每一轮都被问一次。
func (js *jobSyncer) backoff(failures int) time.Duration {
	d := js.cfg.Interval
	for i := 1; i < failures; i++ {
		d *= 2
		if d >= js.cfg.MaxBackoff {
			return js.cfg.MaxBackoff
		}
	}
	return d
}

// Stop ends the background loop and waits for the current tick, if any, to
// finish — a tick is already bounded by batch size, concurrency and
// per-call timeout, so this wait is itself bounded rather than an
// open-ended drain.
//
// Stop 结束后台循环，并等待正在进行的一轮（如果有）跑完——一轮本身已经被批次大小、
// 并发度与单次调用超时约束住，因此这个等待本身是有界的，而不是一次无限期的排空。
func (js *jobSyncer) Stop() {
	close(js.stop)
	<-js.done
}
