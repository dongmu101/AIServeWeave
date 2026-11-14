package jobqueue

import (
	"encoding/json"
	"errors"
	"time"
)

// TaskType is the asynq task type of a queued workflow submission.
const TaskType = "aisw.workflow.submit"

// Task kinds. Runs and image generations share one queue so they keep
// contending fairly for the same ComfyUI nodes.
const (
	KindRun   = "run"
	KindImage = "image"
)

// MaxPayloadBytes bounds one queued task. A larger submission skips the queue
// and is dispatched synchronously, so Redis holds at most MaxLength payloads
// of this size.
//
// MaxPayloadBytes 限定单个排队任务的大小。更大的提交跳过队列、同步派发，因此 Redis
// 至多持有 MaxLength 个这么大的载荷。
const MaxPayloadBytes = 4 << 20

// Task is one queued workflow submission. It carries everything a worker on
// any replica needs to dispatch it: the already-bound graph (so a template
// republished while it waits does not change what runs) and the caller's
// access policies, opaque to this package, which the Dispatcher turns back
// into a scheduling context. The graph holds the caller's prompt: it lives in
// Redis only until the task completes and is never logged.
//
// Task 是一次排队的工作流提交。它携带任一副本上的 worker 派发它所需的一切：已绑定的图
// （排队期间模板被重新发布也不会改变要运行的内容）以及调用方的访问策略——对本包不透明，
// 由 Dispatcher 还原成调度上下文。图里有调用方的提示词：它只在任务完成之前存在于 Redis，
// 从不写入日志。
type Task struct {
	V               int             `json:"v"`
	Kind            string          `json:"kind"`
	JobID           string          `json:"job_id"`
	TenantID        string          `json:"tenant_id,omitempty"`
	WorkflowID      string          `json:"workflow_id"`
	WorkflowVersion string          `json:"workflow_version,omitempty"`
	Graph           json.RawMessage `json:"graph"`
	IdempotencyKey  string          `json:"idempotency_key,omitempty"`
	Access          json.RawMessage `json:"access,omitempty"`
	RequestID       string          `json:"request_id,omitempty"`
	EnqueuedAt      time.Time       `json:"enqueued_at"`
	// Deadline is when the job gives up waiting for capacity and fails with
	// CodeQueueTimeout. It is enforced by the worker, not by asynq.
	Deadline time.Time `json:"deadline"`
}

// State is a queued job's lifecycle as the shared store records it.
type State string

const (
	// StateQueued: waiting for capacity; no backend has seen it.
	StateQueued State = "queued"
	// StateDispatching: a worker recorded its intent and is submitting. A
	// job found here by a later delivery has an unknown outcome.
	StateDispatching State = "dispatching"
	// StateDispatched: a node accepted it; Binding says where.
	StateDispatched State = "dispatched"
	// StateFailed: it never reached a node, or its outcome is unknown.
	StateFailed State = "failed"
	// StateCancelled: the caller cancelled it before any backend saw it.
	StateCancelled State = "cancelled"
)

// Waiting reports whether the job has not been dispatched or decided yet.
func (s State) Waiting() bool { return s == StateQueued || s == StateDispatching }

// Failure codes recorded on a failed or cancelled job.
const (
	// CodeQueueTimeout: no capacity freed up before the deadline.
	CodeQueueTimeout = "queue_timeout"
	// CodeSubmitOutcomeUnknown: a submit may have reached a backend but
	// was not confirmed. It is never resubmitted, which could produce a
	// second generation.
	CodeSubmitOutcomeUnknown = "submit_outcome_unknown"
	// CodeNodeNotAllowed: the caller's access policy excluded every node.
	CodeNodeNotAllowed = "node_not_allowed"
	// CodeCancelled: cancelled while queued.
	CodeCancelled = "cancelled"
	// CodeInternal: the worker failed unexpectedly.
	CodeInternal = "internal_error"
)

// Binding is where a dispatched job runs.
type Binding struct {
	NodeID    string
	RuntimeID string
	Kind      string
	RunID     string
}

// Record is a queued job as the shared store holds it.
type Record struct {
	JobID           string
	TenantID        string
	WorkflowID      string
	WorkflowVersion string
	Kind            string
	State           State
	CancelRequested bool
	Binding         Binding
	ErrorCode       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// BeginResult is the outcome of BeginDispatch.
type BeginResult int

const (
	// Begun: the intent is recorded; the worker may submit.
	Begun BeginResult = iota + 1
	// BeginNotQueued: the job left the queue (dispatched, failed, cancelled
	// or begun by another worker).
	BeginNotQueued
	// BeginCancelled: a cancel was pending, so the job is now cancelled.
	BeginCancelled
)

// DispatchResult is the outcome of Dispatched.
type DispatchResult struct {
	// Recorded is false when the record no longer matched (it expired, or a
	// redelivery already decided the job): the binding was not written.
	Recorded bool
	// CancelRequested: a cancel arrived while the submit was in flight.
	CancelRequested bool
}

// CancelResult is the outcome of Cancel.
type CancelResult int

const (
	// CancelDone: the job was still queued and is now cancelled.
	CancelDone CancelResult = iota + 1
	// CancelRequested: a worker is submitting it; the run is interrupted
	// once the submit succeeds.
	CancelRequested
	// CancelDispatched: it already runs on a node; cancel it there.
	CancelDispatched
	// CancelFinished: it already failed or was cancelled.
	CancelFinished
)

var (
	// ErrQueueFull: the cluster-wide queue length was reached.
	ErrQueueFull = errors.New("jobqueue: queue is full")
	// ErrUnavailable: the queue could not take the job, and it is certain
	// the job was not enqueued, so the caller may dispatch it synchronously.
	ErrUnavailable = errors.New("jobqueue: queue unavailable")
	// ErrIndeterminate: enqueueing failed and it could not be confirmed that
	// the job was withdrawn; the caller must not dispatch it again.
	ErrIndeterminate = errors.New("jobqueue: enqueue outcome unknown")
	// ErrTooLarge: the task exceeds MaxPayloadBytes.
	ErrTooLarge = errors.New("jobqueue: task too large to queue")
	// ErrNotFound: the store holds no record of the job.
	ErrNotFound = errors.New("jobqueue: no such job")
	// ErrRequeue is returned by a worker that held a task without finding
	// capacity, so another replica's worker may pick it up. It never counts
	// as a failure.
	ErrRequeue = errors.New("jobqueue: no capacity yet, requeue")
)
