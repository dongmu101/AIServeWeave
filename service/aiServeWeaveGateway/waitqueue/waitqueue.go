// Package waitqueue is the cluster-wide bounded wait for new chat requests
// (scheduler.WaitQueue). When every candidate a request tried was busy, the
// request takes a ticket in one Redis-held FIFO shared by every Gateway
// replica, and only tickets inside the head window re-run a dispatch pass.
// Dispatch itself never leaves the replica holding the client's connection:
// the queue decides when a waiter may try again, never where it goes, which
// keeps the tunnel's "no forwarding" constraint intact.
//
// Bounds, per "任何一跳都不得无界缓冲":
//   - MaxWaiters caps tickets across the whole cluster; one beyond it is
//     refused at once (the caller answers 429).
//   - MaxWait caps how long one request waits.
//   - Every ticket carries a lease renewed on each poll; a crashed replica's
//     tickets expire and are purged by the next script that runs.
//
// Head-of-line blocking is the price of FIFO across replicas: the head ticket
// may belong to a replica that cannot reach the node that just freed up. When
// no queued request has dispatched for StallAfter, every replica's own oldest
// ticket becomes eligible too, so the queue keeps moving without giving up
// FIFO in the normal case.
//
// Only a request that already found every candidate busy reaches Redis, so an
// unsaturated cluster makes no Redis calls on the request path. New arrivals
// can still take freed capacity ahead of waiters (barging), exactly as with
// the old in-process wait.
//
// A Redis failure aborts the wait and returns the last busy error — an
// immediate 429 — rather than holding the request or failing it differently:
// the queue is an optimization over 429 + Retry-After, never a dependency of
// serving.
//
// waitqueue 是新对话请求的全集群有界等待（scheduler.WaitQueue）。请求尝试过的每个候选
// 都繁忙时，它在一个由所有 Gateway 副本共享、保存在 Redis 中的 FIFO 里取一张票，只有位于
// 头部窗口内的票会重跑派发。派发本身从不离开持有客户端连接的那个副本：队列只决定等待者
// 何时可以再试，从不决定它去哪里，因此隧道「不转发」的约束保持不变。
//
// 各项界限，对应「任何一跳都不得无界缓冲」：
//   - MaxWaiters 限定整个集群的票数；超出的请求立即被拒绝（调用方回答 429）。
//   - MaxWait 限定单个请求的等待时长。
//   - 每张票带一个在每次轮询时续期的租约；崩溃副本的票会过期，并被下一次执行的脚本清除。
//
// 跨副本 FIFO 的代价是队头阻塞：队首的票可能属于一个连不到刚空出来那个节点的副本。若在
// StallAfter 内没有任何排队请求成功派发，每个副本自己最早的那张票也会获得资格，使队列在
// 常态下保持 FIFO 的同时不至于卡死。
//
// 只有已经发现所有候选都繁忙的请求才会进入 Redis，因此未饱和的集群在请求路径上没有任何
// Redis 调用。新到的请求仍可能抢在等待者前面拿到空出的容量（插队），与旧的进程内等待相同。
//
// Redis 故障会终止等待并返回最近一次繁忙错误——即立即 429——而不是继续扣住请求或以其他
// 方式失败：队列是对「429 + Retry-After」的优化，从来不是提供服务的依赖。
package waitqueue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"AIServeWeave/common/runtime"
)

// Defaults applied by New.
const (
	DefaultRetryInterval = 250 * time.Millisecond
	DefaultMaxWaiters    = 64
	DefaultHeadWindow    = 8

	// minLease keeps a ticket alive across a short GC pause or a slow Redis
	// round trip even with a very short retry interval.
	minLease = 5 * time.Second
	// leaveTimeout bounds the best-effort ticket removal after a wait ends;
	// the lease is the backstop if it fails.
	leaveTimeout = time.Second
)

// Wait outcomes, the outcome label of MetricWaitSeconds.
const (
	OutcomeResolved   = "resolved"
	OutcomeCanceled   = "canceled"
	OutcomeTimeout    = "timeout"
	OutcomeLost       = "lost"
	OutcomeStoreError = "store_error"
)

// Config configures a Queue.
type Config struct {
	// Client is the shared Redis. Required by New.
	Client redis.UniversalClient
	// Name isolates this queue's keys; every replica of one fleet must use
	// the same name. Default "chat".
	Name string
	// ReplicaID tags this replica's tickets, for the stall escape. Required.
	ReplicaID string
	// MaxWait bounds one request's wait. Must be positive.
	MaxWait time.Duration
	// RetryInterval is how often a waiter polls. Default 250ms.
	RetryInterval time.Duration
	// MaxWaiters caps waiting requests across the cluster. Default 64.
	MaxWaiters int
	// HeadWindow is how many tickets at the head may run a dispatch pass at
	// once. Default 8.
	HeadWindow int

	Clock   runtime.Clock
	Metrics runtime.Metrics
	Logger  *slog.Logger
}

// pollResult is a ticket's standing after one poll.
type pollResult int

const (
	pollWait pollResult = iota
	pollEligible
	pollLost
)

// ticketStore is the shared FIFO. The Redis implementation is in
// store_redis.go; tests substitute an in-memory one.
type ticketStore interface {
	// join adds ticket unless maxWaiters tickets are already waiting.
	join(ctx context.Context, ticket string, maxWaiters int, lease time.Duration) (bool, error)
	// poll renews ticket's lease and reports whether it may run a pass:
	// inside the head window, or the oldest of its replica (prefix) once no
	// queued request has dispatched for stall.
	poll(ctx context.Context, ticket, prefix string, lease time.Duration, window int, stall time.Duration) (pollResult, error)
	// leave removes ticket; dispatched records progress for the stall escape.
	leave(ctx context.Context, ticket string, dispatched bool) error
}

// Queue is a scheduler.WaitQueue backed by a shared ticket store.
type Queue struct {
	cfg     Config
	store   ticketStore
	clock   runtime.Clock
	logger  *slog.Logger
	metrics recorder
	lease   time.Duration
	stall   time.Duration
	prefix  string
	depth   atomic.Int64
	// lastStoreErrLog throttles store-error logging to one line per interval.
	lastStoreErrLog atomic.Int64
}

// New returns a Redis-backed Queue.
// New 返回一个基于 Redis 的 Queue。
func New(cfg Config) (*Queue, error) {
	if cfg.Client == nil {
		return nil, errors.New("waitqueue: a Redis client is required")
	}
	if cfg.Name == "" {
		cfg.Name = "chat"
	}
	return newQueue(cfg, newRedisStore(cfg.Client, cfg.Name))
}

func newQueue(cfg Config, store ticketStore) (*Queue, error) {
	if cfg.MaxWait <= 0 {
		return nil, errors.New("waitqueue: MaxWait must be positive")
	}
	if cfg.ReplicaID == "" || strings.Contains(cfg.ReplicaID, "|") {
		return nil, errors.New("waitqueue: a replica id without '|' is required")
	}
	if cfg.RetryInterval <= 0 {
		cfg.RetryInterval = DefaultRetryInterval
	}
	if cfg.MaxWaiters <= 0 {
		cfg.MaxWaiters = DefaultMaxWaiters
	}
	if cfg.HeadWindow <= 0 {
		cfg.HeadWindow = DefaultHeadWindow
	}
	if cfg.Clock == nil {
		cfg.Clock = runtime.NewSystemClock()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Queue{
		cfg:     cfg,
		store:   store,
		clock:   cfg.Clock,
		logger:  cfg.Logger,
		metrics: newRecorder(cfg.Metrics),
		lease:   max(10*cfg.RetryInterval, minLease),
		stall:   4 * cfg.RetryInterval,
		prefix:  cfg.ReplicaID + "|",
	}, nil
}

// Wait implements scheduler.WaitQueue.
// Wait 实现 scheduler.WaitQueue。
func (q *Queue) Wait(ctx context.Context, firstErr error, attempt func() (err error, busy bool)) error {
	ticket := q.prefix + newTicketID()
	joined, err := q.store.join(ctx, ticket, q.cfg.MaxWaiters, q.lease)
	if err != nil {
		q.storeError("join", err)
		return firstErr
	}
	if !joined {
		q.metrics.rejected()
		return firstErr
	}
	q.metrics.depth(int(q.depth.Add(1)))
	dispatched := false
	defer func() {
		q.metrics.depth(int(q.depth.Add(-1)))
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaveTimeout)
		defer cancel()
		if err := q.store.leave(lctx, ticket, dispatched); err != nil {
			q.storeError("leave", err)
		}
	}()

	start := q.clock.Now()
	deadline := start.Add(q.cfg.MaxWait)
	lastErr := firstErr
	for {
		remaining := deadline.Sub(q.clock.Now())
		if remaining <= 0 {
			q.metrics.wait(q.clock.Now().Sub(start), OutcomeTimeout)
			return lastErr
		}
		timer, stop := q.clock.NewTimer(min(q.cfg.RetryInterval, remaining))
		select {
		case <-ctx.Done():
			stop()
			q.metrics.wait(q.clock.Now().Sub(start), OutcomeCanceled)
			return ctx.Err()
		case <-timer:
		}

		standing, err := q.store.poll(ctx, ticket, q.prefix, q.lease, q.cfg.HeadWindow, q.stall)
		if err != nil {
			if ctx.Err() != nil {
				q.metrics.wait(q.clock.Now().Sub(start), OutcomeCanceled)
				return ctx.Err()
			}
			q.storeError("poll", err)
			q.metrics.wait(q.clock.Now().Sub(start), OutcomeStoreError)
			return lastErr
		}
		switch standing {
		case pollLost:
			// The lease expired (a pause longer than the lease, or Redis
			// lost the key). Rejoining would jump the queue's order, so the
			// request gets its 429 like any other wait that ran out.
			q.metrics.wait(q.clock.Now().Sub(start), OutcomeLost)
			return lastErr
		case pollWait:
			continue
		}

		err, busy := attempt()
		if err == nil || !busy {
			dispatched = err == nil
			q.metrics.wait(q.clock.Now().Sub(start), OutcomeResolved)
			return err
		}
		lastErr = err
	}
}

// storeError counts a failed store call and logs it at most once per retry
// interval, so a Redis outage under load does not flood the log.
func (q *Queue) storeError(op string, err error) {
	q.metrics.storeError(op)
	now := q.clock.Now().UnixNano()
	last := q.lastStoreErrLog.Load()
	if now-last < int64(q.cfg.RetryInterval) || !q.lastStoreErrLog.CompareAndSwap(last, now) {
		return
	}
	q.logger.Warn("chat wait queue store unavailable; answering 429 without waiting",
		slog.String("op", op), slog.String("error", err.Error()))
}

func newTicketID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
