// Package jobqueue is the Gateway's durable, cluster-wide queue for workflow
// submissions (POST /v1/workflows/{id}/runs with a JSON body) and ComfyUI
// image generations: the "AIServeWeave 队列" docs/architecture.md describes in
// front of ComfyUI. A submission is admitted into Redis and answered at once;
// a worker on whichever replica asynq hands the task to dispatches it when a
// node has capacity. Every Agent tunnels to every replica, so that worker can
// normally reach the node.
//
// Delivery is at least once, but a submission reaches a backend at most once.
// Before submitting, a worker records its intent (queued → dispatching) in
// the shared store; a later delivery that finds an intent without a binding
// knows a previous worker died mid-submit and marks the job
// submit_outcome_unknown instead of resubmitting, because only a job still in
// this queue and never submitted may be rescheduled (architecture.md).
//
// Bounds, per "任何一跳都不得无界缓冲": MaxLength caps admitted jobs across
// the cluster (asynq itself has no length limit), each job carries a deadline
// after which it fails with queue_timeout, and a payload over MaxPayloadBytes
// is not queued at all.
//
// jobqueue 是 Gateway 面向工作流提交（JSON 请求体的 POST /v1/workflows/{id}/runs）与
// ComfyUI 图像生成的持久、全集群队列：即 docs/architecture.md 所述位于 ComfyUI 之前的
// 「AIServeWeave 队列」。提交被准入 Redis 后立即作答；asynq 把任务交给哪个副本，那个副本的
// worker 就在有节点空闲时派发它。每个 Agent 都连接到每个副本，因此该 worker 通常都能到达
// 节点。
//
// 投递是「至少一次」，但一次提交至多到达后端一次。提交之前，worker 先在共享存储中记录意图
// （queued → dispatching）；之后的投递若发现只有意图而没有绑定，就知道上一个 worker 死在
// 提交途中，于是把 job 标为 submit_outcome_unknown 而不是重新提交——因为只有仍在本队列中、
// 从未提交过的任务才可以重新调度（architecture.md）。
//
// 界限，对应「任何一跳都不得无界缓冲」：MaxLength 限定整个集群的已准入 job 数（asynq 本身
// 没有长度限制），每个 job 带一个期限，超过即以 queue_timeout 失败，超过 MaxPayloadBytes
// 的载荷根本不入队。
package jobqueue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"AIServeWeave/common/runtime"
)

// Internal timings, not flags: they bound one delivery and keep the record
// around long enough for a caller to read the outcome.
const (
	// submitTimeout bounds one submit call to a node. A ComfyUI submit is a
	// single POST /prompt, normally well under a second.
	submitTimeout = 30 * time.Second
	// ShutdownTimeout is how long a replica's shutdown waits for in-flight
	// submits. It exceeds submitTimeout, so a submit in flight finishes and
	// records its binding instead of being redelivered and reported as an
	// unknown outcome.
	// ShutdownTimeout 是副本关停时等待在途提交的时长。它大于 submitTimeout，使在途提交
	// 能完成并记录绑定，而不是被重新投递、报告为结果未知。
	ShutdownTimeout = submitTimeout + 5*time.Second
	// holdFor is how long a worker keeps a task in hand re-polling before it
	// hands it back to the queue. Holding instead of letting asynq retry
	// keeps FIFO order, which retries scramble; handing back bounds how long
	// a task stuck on a busy node keeps a worker from tasks behind it.
	// holdFor 是 worker 在交还任务前持有它、反复轮询的时长。持有而不是交给 asynq 重试可保持
	// FIFO 顺序；交还则限定一个卡在忙碌节点上的任务能让 worker 无法处理后面的任务多久。
	holdFor = 10 * time.Second
	// recordTTL keeps a job's record after it leaves the queue, so a caller
	// asking any replica learns how it ended.
	recordTTL = 24 * time.Hour
	// awaitInterval is how often AwaitDispatch re-reads a waiting job.
	awaitInterval = 500 * time.Millisecond
	// awaitErrorBudget is how many consecutive failed reads AwaitDispatch
	// rides out before giving up, so one slow Redis reply does not fail a
	// synchronous image request whose job is still queued.
	awaitErrorBudget = 6
	// depthInterval is how often each replica samples the queue length.
	depthInterval = 10 * time.Second
	// storeTimeout bounds one store call on the request path, so a Redis
	// that stops answering costs a request a second, not the client's
	// retry-and-timeout budget.
	storeTimeout = 2 * time.Second
	// bindingRetryFor bounds how long a worker keeps trying to record the
	// binding of a run a node already accepted.
	bindingRetryFor = 15 * time.Second
)

// Dispatcher submits a queued task on this replica and records the result
// locally. httpapi implements it.
//
// Dispatcher 在本副本上提交一个排队任务并在本地记录结果。由 httpapi 实现。
type Dispatcher interface {
	// Submit dispatches task to a node. Its error is classified with
	// Classify: retryable means certainly unseen by any backend.
	Submit(ctx context.Context, task Task) (Binding, error)
	// Dispatched records a submission that a node accepted.
	Dispatched(task Task, binding Binding)
	// CancelDispatched interrupts a run that was cancelled while it was
	// being submitted.
	CancelDispatched(ctx context.Context, task Task, binding Binding)
}

// Outcome classifies a Submit error.
type Outcome int

const (
	// OutcomeRetry: certainly unseen by any backend (busy, no capable node
	// on this replica, request never delivered); try again later.
	OutcomeRetry Outcome = iota
	// OutcomeDenied: the caller's access policy excludes every node.
	OutcomeDenied
	// OutcomeUnknown: a backend may have seen it; never resubmit.
	OutcomeUnknown
)

// Config configures a Queue.
type Config struct {
	Store     Store
	Transport Transport
	// Classify maps a Submit error onto an Outcome. Required.
	Classify func(error) Outcome
	// ReplicaID prefixes this replica's dispatch intents. Required.
	ReplicaID string
	// MaxWait is the longest a job waits for capacity. Must be positive.
	MaxWait time.Duration
	// MaxLength caps admitted jobs across the cluster. Default 64.
	MaxLength int
	// RetryInterval is how often a held task re-polls. Default 1s.
	RetryInterval time.Duration

	Clock   runtime.Clock
	Metrics runtime.Metrics
	Logger  *slog.Logger
}

// Queue is the durable workflow queue.
type Queue struct {
	cfg        Config
	clock      runtime.Clock
	logger     *slog.Logger
	metrics    recorder
	dispatcher Dispatcher
	heldMu     sync.Mutex
	held       int
	stopping   chan struct{}
	stopOnce   sync.Once
	done       chan struct{}
	started    atomic.Bool
}

// New validates cfg and returns a Queue. Call Start to run workers.
func New(cfg Config) (*Queue, error) {
	if cfg.Store == nil || cfg.Transport == nil || cfg.Classify == nil {
		return nil, errors.New("jobqueue: store, transport and classifier are required")
	}
	if cfg.MaxWait <= 0 {
		return nil, errors.New("jobqueue: MaxWait must be positive")
	}
	if cfg.ReplicaID == "" {
		return nil, errors.New("jobqueue: a replica id is required")
	}
	if cfg.MaxLength <= 0 {
		cfg.MaxLength = 64
	}
	if cfg.RetryInterval <= 0 {
		cfg.RetryInterval = time.Second
	}
	if cfg.Clock == nil {
		cfg.Clock = runtime.NewSystemClock()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Queue{
		cfg:      cfg,
		clock:    cfg.Clock,
		logger:   cfg.Logger,
		metrics:  newRecorder(cfg.Metrics),
		stopping: make(chan struct{}),
		done:     make(chan struct{}),
	}, nil
}

// MaxWait is the configured longest wait for capacity.
func (q *Queue) MaxWait() time.Duration { return q.cfg.MaxWait }

// horizon is how long an admitted job may legitimately stay waiting: its
// wait, plus one held delivery and one submit, plus slack for a worker that
// picks it up late. A job still waiting past it belongs to an enqueue that
// crashed before handing the task to asynq, or to a worker whose final write
// was lost; it is failed with queue_timeout when next read (expire), so no
// record shows "queued" until its TTL.
//
// horizon 是一个已准入 job 合理地保持等待状态的最长时间：等待时长，加一次持有、一次提交，
// 再加一段供 worker 晚取的余量。超过它仍在等待的 job，要么属于在交给 asynq 之前就崩溃的
// 入队，要么属于最终写入丢失的 worker；下次读取时以 queue_timeout 失败（expire），使任何
// 记录都不会一直显示「queued」直到 TTL。
func (q *Queue) horizon() time.Duration { return q.cfg.MaxWait + holdFor + submitTimeout + time.Minute }

// Enqueue admits task and hands it to the transport, returning its position.
// ErrQueueFull, ErrTooLarge and ErrUnavailable all leave the job without an
// asynq task, so it can never be dispatched from the queue and the caller may
// answer or dispatch synchronously; ErrIndeterminate means a task may exist
// and the job must not be dispatched again. The calls are detached from the
// request's cancellation: a client that disconnects mid-enqueue must not
// leave the outcome undecided.
//
// Enqueue 准入 task 并交给传输层，返回它的名次。ErrQueueFull、ErrTooLarge 与
// ErrUnavailable 都表示 job 没有 asynq 任务，永远不会从队列被派发，调用方可以直接作答或
// 同步派发；ErrIndeterminate 表示可能存在任务，不得再次派发。这些调用不随请求取消：
// 入队途中断开的客户端不能让结果悬而未决。
func (q *Queue) Enqueue(ctx context.Context, task Task) (int, error) {
	task.V = 1
	payload, err := json.Marshal(task)
	if err != nil {
		return 0, err
	}
	if len(payload) > MaxPayloadBytes {
		return 0, ErrTooLarge
	}
	ctx = context.WithoutCancel(ctx)
	now := q.clock.Now()
	actx, cancel := context.WithTimeout(ctx, storeTimeout)
	position, err := q.cfg.Store.Admit(actx, Record{
		JobID: task.JobID, TenantID: task.TenantID, WorkflowID: task.WorkflowID,
		WorkflowVersion: task.WorkflowVersion, Kind: task.Kind, CreatedAt: now,
	}, q.cfg.MaxLength, q.horizon(), recordTTL)
	cancel()
	switch {
	case errors.Is(err, ErrQueueFull):
		q.metrics.rejected("full")
		return 0, err
	case errors.Is(err, ErrIndeterminate):
		return 0, err
	case err != nil:
		// Even if the script ran before the reply was lost, the record has
		// no task behind it: it can never be dispatched, and expire ends it.
		// 即使脚本在回复丢失之前已执行，这条记录背后也没有任务：它永远不会被派发，由 expire 收尾。
		q.storeError("admit", err)
		q.metrics.rejected("unavailable")
		return 0, ErrUnavailable
	}
	ectx, cancel := context.WithTimeout(ctx, storeTimeout)
	err = q.cfg.Transport.Enqueue(ectx, task.JobID, payload, holdFor+submitTimeout+10*time.Second)
	cancel()
	if err != nil {
		q.storeError("enqueue", err)
		wctx, cancel := context.WithTimeout(ctx, storeTimeout)
		withdrawn, werr := q.cfg.Store.Withdraw(wctx, task.JobID, q.clock.Now())
		cancel()
		if werr != nil || !withdrawn {
			// The task may have been enqueued after all; a worker reading a
			// withdrawn record does nothing, but one reading a queued record
			// would dispatch it, so the caller must not.
			return 0, ErrIndeterminate
		}
		q.metrics.rejected("unavailable")
		return 0, ErrUnavailable
	}
	return position, nil
}

// expire fails a job that is still waiting past its horizon (see horizon)
// and returns the record as it now reads.
func (q *Queue) expire(ctx context.Context, rec Record) Record {
	if !rec.State.Waiting() || rec.CreatedAt.IsZero() || q.clock.Now().Before(rec.CreatedAt.Add(q.horizon())) {
		return rec
	}
	if ok, err := q.cfg.Store.Fail(ctx, rec.JobID, rec.State, CodeQueueTimeout, q.clock.Now()); err != nil || !ok {
		return rec
	}
	rec.State, rec.ErrorCode, rec.UpdatedAt = StateFailed, CodeQueueTimeout, q.clock.Now()
	return rec
}

// Lookup returns the shared record of jobID.
func (q *Queue) Lookup(ctx context.Context, jobID string) (Record, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	rec, ok, err := q.cfg.Store.Get(ctx, jobID)
	if err != nil {
		q.storeError("get", err)
		return Record{}, false, err
	}
	if !ok {
		return Record{}, false, nil
	}
	return q.expire(ctx, rec), true, nil
}

// Position returns jobID's 1-based position, or 0 when it is not queued.
func (q *Queue) Position(ctx context.Context, jobID string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	return q.cfg.Store.Position(ctx, jobID)
}

// Cancel cancels jobID if it is still queued, and drops its task from the
// transport on a best-effort basis. ErrNotFound when no record exists.
func (q *Queue) Cancel(ctx context.Context, jobID string) (CancelResult, error) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
	defer cancel()
	result, err := q.cfg.Store.Cancel(cctx, jobID, q.clock.Now())
	if err != nil {
		return 0, err
	}
	if result == CancelDone {
		q.metrics.wait(0, "cancelled")
		if err := q.cfg.Transport.Delete(cctx, jobID); err != nil {
			// The worker reads the record before submitting, so a task left
			// behind finds the job cancelled and does nothing.
			q.logger.Debug("dropping a cancelled task failed", slog.String("job_id", jobID), slog.String("error", err.Error()))
		}
	}
	return result, nil
}

// AwaitDispatch blocks until jobID leaves the queue (dispatched, failed or
// cancelled) or ctx ends. onPoll, when set, sees the job's position on every
// poll (every 500ms), so a caller can report changes and keep a stream alive.
// A few consecutive failed reads are ridden out; more return the error.
//
// AwaitDispatch 阻塞到 jobID 离开队列（已派发、失败或取消）或 ctx 结束。onPoll 若设置，
// 会在每次轮询（每 500ms）时看到 job 的名次，供调用方报告变化并保持流的活跃。少数几次
// 连续读取失败会被容忍，更多则返回错误。
func (q *Queue) AwaitDispatch(ctx context.Context, jobID string, onPoll func(position int)) (Record, error) {
	failures := 0
	for {
		sctx, cancel := context.WithTimeout(ctx, storeTimeout)
		rec, position, ok, err := q.cfg.Store.Snapshot(sctx, jobID)
		if err == nil && ok {
			rec = q.expire(sctx, rec)
		}
		cancel()
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return Record{}, ctx.Err()
			}
			if failures++; failures >= awaitErrorBudget {
				q.storeError("await", err)
				return Record{}, err
			}
		case !ok:
			return Record{}, ErrNotFound
		case !rec.State.Waiting():
			return rec, nil
		default:
			failures = 0
			if onPoll != nil {
				onPoll(position)
			}
		}
		timer, stop := q.clock.NewTimer(awaitInterval)
		select {
		case <-ctx.Done():
			stop()
			return rec, ctx.Err()
		case <-timer:
		}
	}
}

// Start runs this replica's workers, dispatching through d, and samples the
// queue length until Shutdown.
func (q *Queue) Start(d Dispatcher) error {
	q.dispatcher = d
	if err := q.cfg.Transport.Start(q.process); err != nil {
		return err
	}
	q.started.Store(true)
	go q.sampleDepth()
	return nil
}

// Shutdown hands held tasks back to the queue and stops the workers. In-flight
// submits finish first (up to ShutdownTimeout), so call it while tunnels and
// the persister are up.
func (q *Queue) Shutdown() {
	q.stopOnce.Do(func() {
		close(q.stopping)
		q.cfg.Transport.Shutdown()
		if q.started.Load() {
			<-q.done
		}
	})
}

// sampleDepth publishes the queue length. It runs on the wall clock rather
// than q.clock: it only feeds a gauge, and a test clock driving the workers
// must not also drive an endless sampling loop.
func (q *Queue) sampleDepth() {
	defer close(q.done)
	ticker := time.NewTicker(depthInterval)
	defer ticker.Stop()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if n, err := q.cfg.Store.Depth(ctx); err == nil {
			q.metrics.depth(n)
		}
		cancel()
		select {
		case <-q.stopping:
			return
		case <-ticker.C:
		}
	}
}

func (q *Queue) addHeld(delta int) {
	q.heldMu.Lock()
	q.held += delta
	q.metrics.held(q.held)
	q.heldMu.Unlock()
}

// errStore marks a delivery that ended on a store failure, so it is handed
// back (ErrRequeue) and the next delivery decides the job from the record.
var errStore = errors.New("jobqueue: store unavailable")

// process is one delivery of a queued task. A decided outcome returns nil, so
// asynq never archives a payload (it holds the prompt); a requeue, or a store
// failure that left the outcome unrecorded, returns an error wrapping
// ErrRequeue, which is not a failure.
//
// process 是一个排队任务的一次投递。已有定论的结果返回 nil，使 asynq 永远不会归档载荷
// （里面有提示词）；交还队列，或因存储失败而结果未记录，返回包装了 ErrRequeue 的错误，
// 它不算失败。
func (q *Queue) process(ctx context.Context, payload []byte) (err error) {
	var task Task
	if jerr := json.Unmarshal(payload, &task); jerr != nil || task.JobID == "" {
		q.logger.Error("dropping an unreadable queued task")
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			q.logger.Error("queued workflow dispatch panicked", slog.String("job_id", task.JobID), slog.Any("panic", r))
			q.failAny(task.JobID, CodeInternal)
			err = nil
		}
		if errors.Is(err, errStore) {
			err = fmt.Errorf("%w: %w", ErrRequeue, err)
		}
	}()

	gctx, cancel := context.WithTimeout(ctx, storeTimeout)
	rec, ok, gerr := q.cfg.Store.Get(gctx, task.JobID)
	cancel()
	if gerr != nil {
		q.storeError("get", gerr)
		return errStore
	}
	if !ok {
		return nil // expired or withdrawn
	}
	switch rec.State {
	case StateQueued:
	case StateDispatching:
		// A previous delivery recorded its intent and never confirmed:
		// it may have reached a backend, so it is never sent again.
		return q.fail(task, StateDispatching, CodeSubmitOutcomeUnknown)
	default:
		return nil // dispatched, failed or cancelled already
	}

	q.addHeld(1)
	defer q.addHeld(-1)

	holdUntil := q.clock.Now().Add(holdFor)
	for {
		now := q.clock.Now()
		if !now.Before(task.Deadline) {
			return q.fail(task, StateQueued, CodeQueueTimeout)
		}
		intent := q.cfg.ReplicaID + ":" + newIntentID()
		begun, berr := q.begin(ctx, task.JobID, intent)
		if berr != nil {
			return berr
		}
		if begun != Begun {
			return nil // cancelled, or decided by someone else
		}

		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), submitTimeout)
		binding, serr := q.dispatcher.Submit(sctx, task)
		cancel()
		if serr == nil {
			return q.dispatched(task, intent, binding)
		}
		switch q.cfg.Classify(serr) {
		case OutcomeDenied:
			return q.fail(task, StateDispatching, CodeNodeNotAllowed)
		case OutcomeUnknown:
			q.logger.Warn("queued workflow submit outcome unknown; not resubmitting",
				slog.String("job_id", task.JobID), slog.String("error", serr.Error()))
			return q.fail(task, StateDispatching, CodeSubmitOutcomeUnknown)
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
		cancelled, rerr := q.cfg.Store.Requeue(rctx, task.JobID, intent, q.clock.Now())
		cancel()
		if rerr != nil {
			// The record still says dispatching with this intent. The
			// redelivery will treat it as an unknown outcome — overly
			// cautious here, since this submit certainly reached no
			// backend, but it ends the job visibly instead of leaving it
			// looking queued.
			q.storeError("requeue", rerr)
			return errStore
		}
		if cancelled {
			q.metrics.wait(q.clock.Now().Sub(task.EnqueuedAt), "cancelled")
			return nil
		}
		if !q.clock.Now().Add(q.cfg.RetryInterval).Before(holdUntil) {
			return ErrRequeue
		}
		timer, stop := q.clock.NewTimer(q.cfg.RetryInterval)
		select {
		case <-ctx.Done():
			stop()
			return ErrRequeue
		case <-q.stopping:
			stop()
			return ErrRequeue
		case <-timer:
		}
	}
}

// begin records the dispatch intent, retrying a failed call with the same
// intent (the script is idempotent for it) so a lost reply does not leave a
// never-submitted job looking mid-submit. If it still fails, it tries to
// release the intent before handing the task back.
func (q *Queue) begin(ctx context.Context, jobID, intent string) (BeginResult, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
		result, err := q.cfg.Store.BeginDispatch(bctx, jobID, intent, q.clock.Now())
		cancel()
		if err == nil {
			return result, nil
		}
		lastErr = err
	}
	q.storeError("begin", lastErr)
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
	_, _ = q.cfg.Store.Requeue(rctx, jobID, intent, q.clock.Now())
	cancel()
	return 0, errStore
}

// dispatched records a node's acceptance. After a node accepted the run it
// is never submitted again, whatever else fails here. Recording the binding
// is retried for a bounded time; if Redis stays unreachable the task is
// handed back, and the redelivery marks the job submit_outcome_unknown — the
// run itself goes on, and this replica's local job store tracks it.
//
// dispatched 记录节点接单。节点接单之后，无论这里还有什么失败，这次运行都不会再被提交。
// 记录绑定会在有界时间内重试；若 Redis 一直不可达则交还任务，重新投递时把 job 标为
// submit_outcome_unknown——运行本身继续，由本副本的本地 job 表跟踪。
func (q *Queue) dispatched(task Task, intent string, binding Binding) error {
	var result DispatchResult
	var err error
	backoff := 250 * time.Millisecond
	deadline := q.clock.Now().Add(bindingRetryFor)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
		result, err = q.cfg.Store.Dispatched(ctx, task.JobID, intent, binding, q.clock.Now())
		cancel()
		if err == nil || !q.clock.Now().Add(backoff).Before(deadline) {
			break
		}
		timer, stop := q.clock.NewTimer(backoff)
		<-timer
		stop()
		backoff = min(2*backoff, 4*time.Second)
	}
	q.dispatcher.Dispatched(task, binding)
	if err != nil {
		q.storeError("dispatched", err)
		q.logger.Error("recording a queued workflow's binding failed; other replicas cannot see where it runs",
			slog.String("job_id", task.JobID), slog.String("node_id", binding.NodeID))
		return errStore
	}
	if !result.Recorded {
		q.logger.Warn("a queued workflow was dispatched after its record was decided elsewhere",
			slog.String("job_id", task.JobID), slog.String("node_id", binding.NodeID))
	}
	q.metrics.wait(q.clock.Now().Sub(task.EnqueuedAt), "dispatched")
	if result.CancelRequested {
		ctx, cancel := context.WithTimeout(context.Background(), submitTimeout)
		q.dispatcher.CancelDispatched(ctx, task, binding)
		cancel()
	}
	return nil
}

// fail records a terminal failure. A store failure returns errStore, so the
// task is handed back and the next delivery decides it again instead of the
// job staying "queued" with nothing left to process it.
func (q *Queue) fail(task Task, from State, code string) error {
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	if _, err := q.cfg.Store.Fail(ctx, task.JobID, from, code, q.clock.Now()); err != nil {
		q.storeError("fail", err)
		return errStore
	}
	outcome := "failed"
	switch code {
	case CodeQueueTimeout:
		outcome = "timeout"
	case CodeSubmitOutcomeUnknown:
		outcome = "unknown"
	}
	q.metrics.wait(q.clock.Now().Sub(task.EnqueuedAt), outcome)
	return nil
}

// failAny fails a job whichever waiting state it is in.
func (q *Queue) failAny(jobID string, code string) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	for _, from := range []State{StateDispatching, StateQueued} {
		if ok, err := q.cfg.Store.Fail(ctx, jobID, from, code, q.clock.Now()); err != nil || ok {
			return
		}
	}
}

func (q *Queue) storeError(op string, err error) {
	q.metrics.storeError(op)
	q.logger.Warn("workflow queue store call failed", slog.String("op", op), slog.String("error", err.Error()))
}

func newIntentID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
