package scheduler

import (
	"context"
	"errors"

	"AIServeWeave/common/runtime"
)

// WaitQueue is the bounded wait new Chat, ChatStream and Messages requests
// enter once every candidate they tried was busy. Capacity lives on the
// Agents, not in the queue, so waiting means re-running one dispatch pass
// until it stops finding every candidate busy; the queue decides when a
// waiter may run a pass, and for how long it may wait, never where the
// request goes. Dispatch always stays on the replica that holds the
// client's connection.
//
// Wait re-runs attempt until attempt succeeds, fails without reporting busy,
// ctx ends, or the queue's own bounds run out. firstErr is the failure of
// the pass that found every candidate busy; an implementation returns it
// unchanged when the queue refuses entry, and the latest busy failure on
// timeout. attempt keeps its own results; Wait only reports the final error.
// The production implementation is the Redis-backed cluster-wide FIFO in the
// waitqueue package; the scheduler does not import Redis.
//
// WaitQueue 是新 Chat、ChatStream 与 Messages 请求在所尝试的候选全部繁忙后进入的有界
// 等待。容量在 Agent 上，不在队列里，所以等待的方式是重跑一轮派发，直到不再发现所有候选
// 都忙；队列决定等待者何时可以跑一轮、最多等多久，从不决定请求去哪里。派发始终留在持有
// 客户端连接的那个副本上。
//
// Wait 重跑 attempt，直到 attempt 成功、以非繁忙原因失败、ctx 结束或队列自身的界用尽。
// firstErr 是发现所有候选都忙的那一轮的失败；队列拒绝进入时实现原样返回它，超时返回最近
// 一次繁忙失败。attempt 自己保存结果，Wait 只报告最终错误。生产实现是 waitqueue 包中
// 基于 Redis 的全集群 FIFO；调度器本身不 import Redis。
type WaitQueue interface {
	Wait(ctx context.Context, firstErr error, attempt func() (err error, busy bool)) error
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
