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
	// submitTimeout bounds one submit call to a node.
	submitTimeout = 60 * time.Second
	// holdFor is how long a worker keeps a task in hand re-polling before it
	// hands it back to the queue for another replica. Holding instead of
	// letting asynq retry preserves FIFO order, which retries scramble.
	holdFor = 30 * time.Second
	// recordTTL keeps a job's record after it leaves the queue, so a caller
	// asking any replica learns how it ended.
	recordTTL = 24 * time.Hour
	// awaitInterval is how often AwaitDispatch re-reads a waiting job.
	awaitInterval = 500 * time.Millisecond
	// depthInterval is how often each replica samples the queue length.
	depthInterval = 10 * time.Second
	// bindingAttempts bounds retries of recording a binding after a node
	// already accepted the run.
	bindingAttempts = 3
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
	// on this replica, connection never established); try again later.
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
	held       atomic.Int64
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

// horizon is how long an admitted entry may legitimately stay admitted: its
// wait, plus one held delivery and one submit. Older entries belong to a
// worker that vanished without updating the record and are purged so they do
// not hold queue length forever.
func (q *Queue) horizon() time.Duration { return q.cfg.MaxWait + holdFor + submitTimeout + time.Minute }

// Enqueue admits task and hands it to the transport, returning its position.
// ErrQueueFull, ErrTooLarge and ErrUnavailable all leave the job unqueued, so
// the caller may answer or dispatch synchronously; ErrIndeterminate means it
// may be queued and must not be dispatched again.
//
// Enqueue 准入 task 并交给传输层，返回它的名次。ErrQueueFull、ErrTooLarge 与
// ErrUnavailable 都表示 job 未入队，调用方可以直接作答或同步派发；ErrIndeterminate 表示
// 它可能已入队，不得再次派发。
func (q *Queue) Enqueue(ctx context.Context, task Task) (int, error) {
	task.V = 1
	payload, err := json.Marshal(task)
	if err != nil {
		return 0, err
	}
	if len(payload) > MaxPayloadBytes {
		return 0, ErrTooLarge
	}
	now := q.clock.Now()
	position, err := q.cfg.Store.Admit(ctx, Record{
		JobID: task.JobID, TenantID: task.TenantID, WorkflowID: task.WorkflowID,
		WorkflowVersion: task.WorkflowVersion, Kind: task.Kind, CreatedAt: now,
	}, q.cfg.MaxLength, q.horizon(), recordTTL)
	switch {
	case errors.Is(err, ErrQueueFull):
		q.metrics.rejected("full")
		return 0, err
	case errors.Is(err, ErrIndeterminate):
		return 0, err
	case err != nil:
		q.storeError("admit", err)
		q.metrics.rejected("unavailable")
		return 0, ErrUnavailable
	}
	if err := q.cfg.Transport.Enqueue(ctx, task.JobID, payload, holdFor+submitTimeout+10*time.Second); err != nil {
		q.storeError("enqueue", err)
		withdrawn, werr := q.cfg.Store.Withdraw(context.WithoutCancel(ctx), task.JobID, q.clock.Now())
		if werr != nil || !withdrawn {
			return 0, ErrIndeterminate
		}
		q.metrics.rejected("unavailable")
		return 0, ErrUnavailable
	}
	return position, nil
}

// Lookup returns the shared record of jobID.
func (q *Queue) Lookup(ctx context.Context, jobID string) (Record, bool, error) {
	rec, ok, err := q.cfg.Store.Get(ctx, jobID)
	if err != nil {
		q.storeError("get", err)
	}
	return rec, ok, err
}

// Position returns jobID's 1-based position, or 0 when it is not queued.
func (q *Queue) Position(ctx context.Context, jobID string) (int, error) {
	return q.cfg.Store.Position(ctx, jobID)
}

// Cancel cancels jobID if it is still queued, and drops its task from the
// transport on a best-effort basis.
func (q *Queue) Cancel(ctx context.Context, jobID string) (CancelResult, error) {
	result, err := q.cfg.Store.Cancel(ctx, jobID, q.clock.Now())
	if err != nil {
		return 0, err
	}
	if result == CancelDone {
		q.metrics.wait(0, "cancelled")
		if err := q.cfg.Transport.Delete(context.WithoutCancel(ctx), jobID); err != nil {
			// The worker reads the record before submitting, so a task left
			// behind finds the job cancelled and does nothing.
			q.logger.Debug("dropping a cancelled task failed", slog.String("job_id", jobID), slog.String("error", err.Error()))
		}
	}
	return result, nil
}

// AwaitDispatch blocks until jobID leaves the queue (dispatched, failed or
// cancelled) or ctx ends, reporting each position change to onPosition.
//
// AwaitDispatch 阻塞到 jobID 离开队列（已派发、失败或取消）或 ctx 结束，每次名次变化都
// 报告给 onPosition。
func (q *Queue) AwaitDispatch(ctx context.Context, jobID string, onPosition func(int)) (Record, error) {
	last := -1
	for {
		rec, ok, err := q.cfg.Store.Get(ctx, jobID)
		if err != nil {
			return Record{}, err
		}
		if !ok {
			return Record{}, ErrNotFound
		}
		if !rec.State.Waiting() {
			return rec, nil
		}
		if onPosition != nil {
			if position, err := q.cfg.Store.Position(ctx, jobID); err == nil && position != last {
				last = position
				onPosition(position)
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
// submits finish first, so call it while tunnels and the persister are up.
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

// process is one delivery of a queued task. Terminal outcomes return nil, so
// asynq never archives a payload (it holds the prompt); only a requeue
// returns an error, and that error is not a failure.
//
// process 是一个排队任务的一次投递。终态结果一律返回 nil，使 asynq 永远不会归档载荷（里面
// 有提示词）；只有弹回队列会返回错误，而那个错误不算失败。
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
	}()

	rec, ok, err := q.cfg.Store.Get(ctx, task.JobID)
	if err != nil {
		q.storeError("get", err)
		return fmt.Errorf("%w: reading the record failed", ErrRequeue)
	}
	if !ok {
		return nil // expired or withdrawn
	}
	switch rec.State {
	case StateQueued:
	case StateDispatching:
		// A previous delivery recorded its intent and never confirmed:
		// it may have reached a backend, so it is never sent again.
		q.fail(task, StateDispatching, CodeSubmitOutcomeUnknown)
		return nil
	default:
		return nil // dispatched, failed or cancelled already
	}

	q.metrics.held(int(q.held.Add(1)))
	defer func() { q.metrics.held(int(q.held.Add(-1))) }()

	holdUntil := q.clock.Now().Add(holdFor)
	for {
		now := q.clock.Now()
		if !now.Before(task.Deadline) {
			q.fail(task, StateQueued, CodeQueueTimeout)
			return nil
		}
		intent := q.cfg.ReplicaID + ":" + newIntentID()
		begun, err := q.cfg.Store.BeginDispatch(ctx, task.JobID, intent, now)
		if err != nil {
			q.storeError("begin", err)
			return fmt.Errorf("%w: recording the intent failed", ErrRequeue)
		}
		if !begun {
			return nil // cancelled meanwhile
		}

		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), submitTimeout)
		binding, serr := q.dispatcher.Submit(sctx, task)
		cancel()
		if serr == nil {
			q.dispatched(task, intent, binding)
			return nil
		}
		switch q.cfg.Classify(serr) {
		case OutcomeDenied:
			q.fail(task, StateDispatching, CodeNodeNotAllowed)
			return nil
		case OutcomeUnknown:
			q.logger.Warn("queued workflow submit outcome unknown; not resubmitting",
				slog.String("job_id", task.JobID), slog.String("error", serr.Error()))
			q.fail(task, StateDispatching, CodeSubmitOutcomeUnknown)
			return nil
		}
		if err := q.cfg.Store.Requeue(context.WithoutCancel(ctx), task.JobID, intent, q.clock.Now()); err != nil {
			// The record still says dispatching with this intent. The
			// redelivery will treat it as an unknown outcome — overly
			// cautious here, since this submit certainly reached no
			// backend, but it ends the job visibly instead of leaving it
			// looking queued until its record expires.
			q.storeError("requeue", err)
			return fmt.Errorf("%w: releasing the intent failed", ErrRequeue)
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

// dispatched records a node's acceptance. After a node accepted the run the
// task is done whatever else fails here: resubmitting would run it twice.
func (q *Queue) dispatched(task Task, intent string, binding Binding) {
	var cancelRequested bool
	var err error
	for attempt := 0; attempt < bindingAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cancelRequested, err = q.cfg.Store.Dispatched(ctx, task.JobID, intent, binding, q.clock.Now())
		cancel()
		if err == nil {
			break
		}
	}
	if err != nil {
		q.storeError("dispatched", err)
		q.logger.Error("recording a queued workflow's binding failed; other replicas cannot see it until recovery",
			slog.String("job_id", task.JobID), slog.String("node_id", binding.NodeID))
	}
	q.metrics.wait(q.clock.Now().Sub(task.EnqueuedAt), "dispatched")
	q.dispatcher.Dispatched(task, binding)
	if cancelRequested {
		ctx, cancel := context.WithTimeout(context.Background(), submitTimeout)
		q.dispatcher.CancelDispatched(ctx, task, binding)
		cancel()
	}
}

func (q *Queue) fail(task Task, from State, code string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := q.cfg.Store.Fail(ctx, task.JobID, from, code, q.clock.Now()); err != nil {
		q.storeError("fail", err)
	}
	outcome := "failed"
	switch code {
	case CodeQueueTimeout:
		outcome = "timeout"
	case CodeSubmitOutcomeUnknown:
		outcome = "unknown"
	}
	q.metrics.wait(q.clock.Now().Sub(task.EnqueuedAt), outcome)
}

// failAny fails a job whichever waiting state it is in.
func (q *Queue) failAny(jobID string, code string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
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
