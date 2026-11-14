package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"

	"AIServeWeave/common/runtime"
)

// waitQueue is the bounded wait shared by workflow submissions and new chat
// or Messages requests. Capacity lives on the Agents, not here, so waiting
// means re-running one dispatch pass every interval until it stops finding
// every candidate busy. The queue bounds the wait on two independent axes,
// per the rule "任何一跳都不得无界缓冲": maxWait limits how long one request
// waits, and the waiters semaphore limits how many wait at once. A request
// that finds the semaphore full is rejected immediately instead of becoming
// an unbounded third axis.
//
// waitQueue 是工作流提交与新对话、新 Messages 请求共用的有界等待。容量在 Agent 上，
// 不在这里，所以等待的方式是每隔 interval 重跑一轮派发，直到不再发现所有候选都忙。
// 队列在两条彼此独立的轴上设界，对应「任何一跳都不得无界缓冲」：maxWait 限一次请求的
// 等待时长，waiters 信号量限同时等待的请求数。发现信号量已满的请求被立即拒绝，不会成为
// 第三条无界的轴。
type waitQueue struct {
	clock    runtime.Clock
	maxWait  time.Duration
	interval time.Duration
	waiters  *semaphore.Weighted
	depth    atomic.Int64
	observe  queueObserver
}

// queueObserver receives one queue's measurements.
// queueObserver 接收一个队列的观测值。
type queueObserver struct {
	depth    func(int)
	wait     func(time.Duration, string)
	rejected func()
}

// newWaitQueue returns nil, which disables queueing, unless maxWait is
// positive. A non-positive interval or maxWaiters uses the given default.
//
// newWaitQueue 在 maxWait 不为正时返回 nil，即关闭排队。interval 或 maxWaiters 不为正时
// 使用给定的默认值。
func newWaitQueue(clock runtime.Clock, maxWait, interval, defaultInterval time.Duration, maxWaiters, defaultMaxWaiters int, observe queueObserver) *waitQueue {
	if maxWait <= 0 {
		return nil
	}
	if interval <= 0 {
		interval = defaultInterval
	}
	if maxWaiters <= 0 {
		maxWaiters = defaultMaxWaiters
	}
	return &waitQueue{clock: clock, maxWait: maxWait, interval: interval, waiters: semaphore.NewWeighted(int64(maxWaiters)), observe: observe}
}

// wait re-runs attempt every interval until attempt succeeds, fails without
// reporting busy, ctx ends, or maxWait runs out. firstErr is the failure of
// the pass that found every candidate busy; it is returned unchanged when
// the queue is full, and the latest busy failure is returned on timeout.
// attempt keeps its own results; wait only reports the final error.
//
// wait 每隔 interval 重跑一次 attempt，直到 attempt 成功、以非繁忙原因失败、ctx 结束或
// maxWait 用尽。firstErr 是发现所有候选都忙的那一轮的失败：队列已满时原样返回它，超时
// 返回最近一次繁忙失败。attempt 自己保存结果，wait 只报告最终错误。
func (q *waitQueue) wait(ctx context.Context, firstErr error, attempt func() (err error, busy bool)) error {
	if !q.waiters.TryAcquire(1) {
		q.observe.rejected()
		return firstErr
	}
	q.observe.depth(int(q.depth.Add(1)))
	defer func() {
		q.waiters.Release(1)
		q.observe.depth(int(q.depth.Add(-1)))
	}()

	start := q.clock.Now()
	deadline := start.Add(q.maxWait)
	lastErr := firstErr
	for {
		remaining := deadline.Sub(q.clock.Now())
		if remaining <= 0 {
			q.observe.wait(q.clock.Now().Sub(start), queueOutcomeTimeout)
			return lastErr
		}
		timer, stop := q.clock.NewTimer(min(q.interval, remaining))
		select {
		case <-ctx.Done():
			stop()
			q.observe.wait(q.clock.Now().Sub(start), queueOutcomeCanceled)
			return ctx.Err()
		case <-timer:
		}

		err, busy := attempt()
		if err == nil || !busy {
			q.observe.wait(q.clock.Now().Sub(start), queueOutcomeResolved)
			return err
		}
		lastErr = err
	}
}

// busyError reports whether err means the candidate had no free capacity
// right now: the Gateway found no idle tunnel slot, or the Agent's runtime
// limiter or CLI session pool was full. Only this failure is worth waiting
// for; a rate limit lasts minutes to hours and an outage needs no queue.
//
// busyError 报告 err 是否表示候选此刻没有空闲容量：Gateway 找不到空闲隧道槽，或 Agent
// 的运行时限流器、CLI 会话池已满。只有这种失败值得等待；限流会持续几分钟到几小时，故障
// 也不需要排队。
func busyError(err error) bool {
	var rtErr *runtime.RuntimeError
	return errors.As(err, &rtErr) && rtErr.Code == runtime.ErrorBackpressure
}

// busyPass tracks whether every candidate one dispatch pass tried failed
// because it was busy. Skipped candidates (cooling, not allowed) do not
// count either way.
//
// busyPass 记录一轮派发尝试过的每个候选是否都因繁忙而失败。被跳过的候选（冷却中、未授权）
// 不计入。
type busyPass struct {
	tried, busy int
}

func (p *busyPass) record(err error) {
	p.tried++
	if busyError(err) {
		p.busy++
	}
}

func (p *busyPass) allBusy() bool { return p.tried > 0 && p.busy == p.tried }
