package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"AIServeWeave/common/flowdef"
	"AIServeWeave/common/idgen"
	"AIServeWeave/service/aiServeWeaveGateway/flow"
)

// Studio flow run handling on this replica.
//
// 本副本上 Studio 编排运行的处理参数。
const (
	// studioRunLease is the lease this replica holds a run under, renewed
	// every studioRunRenewEvery; a replica that dies leaves its runs to read
	// as interrupted within a lease.
	// studioRunLease 是本副本持有运行的租约，每 studioRunRenewEvery 续一次；副本宕机后，
	// 其运行在一个租约内读作 interrupted。
	studioRunLease      = 60 * time.Second
	studioRunRenewEvery = 20 * time.Second
	// The control plane's bounds on a step's inputs and outputs (each) and on
	// a run's outputs, as encoded JSON; values are cut to fit before writing.
	// 控制面对步骤的输入与输出（各自）以及运行输出的上限，按编码后的 JSON 计；写入前把取值
	// 截断到上限内。
	maxStudioStepJSON      = 256 << 10
	maxStudioRunOutputJSON = 1 << 20
	studioStepWriteTries   = 3
	studioFinishRetryWait  = 2 * time.Second
	// studioSubscriberBuffer is how far a watcher may fall behind before it
	// is dropped; it reconnects and starts again from a snapshot.
	// studioSubscriberBuffer 是观察者最多可以落后的帧数，超过即被断开；它重连后从快照
	// 重新开始。
	studioSubscriberBuffer = 256
	studioRunIDPrefix      = "frn_"
)

// Why a run's context ended, when this replica ended it.
//
// 运行的 context 结束的原因（当由本副本结束时）。
var (
	errStudioRunCanceled = errors.New("studio: run canceled by its owner")
	errStudioRunLost     = errors.New("studio: run no longer held by this replica")
	errStudioShutdown    = errors.New("studio: gateway shutting down")
)

// studioRunner executes Studio flow runs on this replica and tracks the ones
// in progress, so their owners can watch, read and cancel them here.
//
// studioRunner 在本副本上执行 Studio 编排运行，并跟踪进行中的运行，使其所有者能在这里
// 观察、读取与取消它们。
type studioRunner struct {
	h       *handlers
	store   StudioFlowStore
	replica string
	logger  *slog.Logger

	mu      sync.Mutex
	active  map[string]*studioActiveRun
	closing chan struct{}
	closed  bool
	wg      sync.WaitGroup
}

// newStudioRunner returns a runner holding runs as replica plus a per-process
// suffix: a restarted process under the same name must not be able to write
// to runs its predecessor started.
//
// newStudioRunner 返回一个以 replica 加进程级后缀持有运行的执行器：同名重启的进程不得能够
// 写入其前任启动的运行。
func newStudioRunner(h *handlers, store StudioFlowStore, replica string, logger *slog.Logger) *studioRunner {
	if replica == "" {
		replica = "gateway"
	}
	if len(replica) > 96 {
		replica = replica[:96]
	}
	return &studioRunner{
		h: h, store: store, replica: replica + "#" + idgen.New()[:12], logger: logger,
		active: map[string]*studioActiveRun{}, closing: make(chan struct{}),
	}
}

// close cancels every run in progress and waits for them to stop. The runs
// are not finished in storage: their leases lapse and they read as
// interrupted, which is what happened to them.
//
// close 取消所有进行中的运行并等待它们停止。这些运行不会在存储中写入终态：它们的租约
// 失效、读作 interrupted，这正是它们的实际遭遇。
func (rn *studioRunner) close() {
	rn.mu.Lock()
	if !rn.closed {
		rn.closed = true
		close(rn.closing)
		for _, run := range rn.active {
			run.cancel(errStudioShutdown)
		}
	}
	rn.mu.Unlock()
	rn.wg.Wait()
}

// studioActiveRun is one run executing here: what was stored when it
// started, plus the progress so far, kept in the form it is persisted in.
//
// studioActiveRun 是在这里执行的一次运行：启动时存储的内容，加上迄今为止的进度，以持久化
// 时的形式保存。
type studioActiveRun struct {
	owner  StudioOwner
	cancel context.CancelCauseFunc

	mu      sync.Mutex
	run     StudioRun
	running map[string]*strings.Builder // text so far of model calls in flight
	order   []string                    // running nodes, in start order
	done    bool
	subs    map[chan studioRunFrame]struct{}
}

// studioRunFrame is one SSE frame of a run's event stream.
// studioRunFrame 是运行事件流中的一个 SSE 帧。
type studioRunFrame struct {
	event   string
	payload any
}

// view returns a copy of the run as it stands.
// view 返回运行当前状态的副本。
func (a *studioActiveRun) view() (StudioRun, []studioRunningJSON) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.viewLocked()
}

func (a *studioActiveRun) viewLocked() (StudioRun, []studioRunningJSON) {
	run := a.run
	run.Steps = append([]StudioRunStep(nil), a.run.Steps...)
	running := make([]studioRunningJSON, 0, len(a.order))
	for _, id := range a.order {
		running = append(running, studioRunningJSON{NodeID: id, Text: a.running[id].String()})
	}
	return run, running
}

// subscribe registers a watcher and returns the snapshot it starts from,
// atomically with respect to new events. A run that is already done returns
// a nil channel.
//
// subscribe 注册一个观察者并返回它起步的快照，与新事件之间是原子的。已结束的运行返回 nil
// channel。
func (a *studioActiveRun) subscribe() (StudioRun, []studioRunningJSON, chan studioRunFrame) {
	a.mu.Lock()
	defer a.mu.Unlock()
	run, running := a.viewLocked()
	if a.done {
		return run, running, nil
	}
	ch := make(chan studioRunFrame, studioSubscriberBuffer)
	a.subs[ch] = struct{}{}
	return run, running, ch
}

func (a *studioActiveRun) unsubscribe(ch chan studioRunFrame) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.subs[ch]; ok {
		delete(a.subs, ch)
		close(ch)
	}
}

// publishLocked hands a frame to every watcher, dropping one whose buffer is
// full rather than stalling the run for it. Callers hold a.mu.
//
// publishLocked 把一帧交给每个观察者；缓冲已满的观察者被断开，而不是为它拖住运行。调用方
// 持有 a.mu。
func (a *studioActiveRun) publishLocked(event string, payload any) {
	for ch := range a.subs {
		select {
		case ch <- studioRunFrame{event, payload}:
		default:
			delete(a.subs, ch)
			close(ch)
		}
	}
}

// lookup returns owner's run id if it executes here.
// lookup 在 owner 的运行 id 于本副本执行时返回它。
func (rn *studioRunner) lookup(owner StudioOwner, id string) (*studioActiveRun, bool) {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	run, ok := rn.active[id]
	if !ok || run.owner != owner {
		return nil, false
	}
	return run, true
}

// start records run id in storage and starts executing it. files are the
// run's input files, already stored under id. ctx carries the
// member's identity and scheduling access; its cancellation is not inherited,
// so the run outlives the request that started it.
//
// start 在存储中记录运行 id 并开始执行。files 是该运行的输入文件，已存放在 id 之下。
// ctx 携带成员的身份与调度权限；它的取消不被继承，因此运行比启动它的请求活得更久。
func (rn *studioRunner) start(ctx context.Context, owner StudioOwner, id, flowID string, version int64, inputs json.RawMessage, def flowdef.Definition, rawInputs map[string]json.RawMessage, files map[string]flowdef.FileValue) (StudioRun, error) {
	rn.mu.Lock()
	closed := rn.closed
	rn.mu.Unlock()
	if closed {
		return StudioRun{}, errStudioShutdown
	}
	engine, err := flow.New(flow.Config{Dispatcher: studioFlowDispatcher{h: rn.h, session: id, files: files}, Clock: rn.h.clock})
	if err != nil {
		return StudioRun{}, err
	}
	plan, err := engine.Plan(def, rawInputs)
	if err != nil {
		return StudioRun{}, err
	}
	stored, err := rn.store.StartRun(ctx, owner, StudioRunStart{
		ID: id, FlowID: flowID, FlowVersion: version, Inputs: inputs, ReplicaID: rn.replica, Lease: studioRunLease,
	})
	if err != nil {
		return StudioRun{}, err
	}

	runCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	active := &studioActiveRun{owner: owner, cancel: cancel, run: stored, running: map[string]*strings.Builder{}, subs: map[chan studioRunFrame]struct{}{}}
	rn.mu.Lock()
	if rn.closed {
		rn.mu.Unlock()
		cancel(errStudioShutdown)
		return StudioRun{}, errStudioShutdown
	}
	rn.active[id] = active
	rn.wg.Add(1)
	rn.mu.Unlock()
	go rn.execute(runCtx, cancel, active, plan, len(def.Nodes))
	return stored, nil
}

// execute runs plan and persists its progress, then forgets the run.
// execute 运行 plan 并持久化其进度，然后忘掉这次运行。
func (rn *studioRunner) execute(ctx context.Context, cancel context.CancelCauseFunc, active *studioActiveRun, plan *flow.Plan, nodes int) {
	defer rn.wg.Done()
	defer cancel(nil)
	owner, id := active.owner, active.run.ID
	store := context.WithoutCancel(ctx)

	renewed := make(chan struct{})
	leaseDone := make(chan struct{})
	go rn.holdLease(store, owner, id, cancel, renewed, leaseDone)

	steps := make(chan StudioRunStep, nodes+1)
	written := make(chan struct{})
	go rn.writeSteps(store, owner, id, cancel, steps, written)

	result := plan.Run(ctx, func(e flow.Event) { rn.observe(active, e, steps) })
	close(steps)
	<-written

	cause := context.Cause(ctx)
	if !errors.Is(cause, errStudioRunLost) && !errors.Is(cause, errStudioShutdown) {
		rn.finish(store, owner, id, result)
	}
	close(renewed)
	<-leaseDone

	active.mu.Lock()
	active.done = true
	active.run.Status, active.run.ErrorCode, active.run.FailedNode = string(result.Status), result.Error, result.FailedNode
	if errors.Is(cause, errStudioRunLost) || errors.Is(cause, errStudioShutdown) {
		active.run.Status = "interrupted"
	}
	active.run.Outputs = fitValues(result.Outputs, maxStudioRunOutputJSON)
	active.run.PromptTokens, active.run.CompletionTokens = result.Usage.PromptTokens, result.Usage.CompletionTokens
	finished := result.FinishedAt
	active.run.FinishedAt = &finished
	active.publishLocked("run_finished", renderRunFinished(active.run))
	for ch := range active.subs {
		close(ch)
	}
	active.subs = nil
	active.mu.Unlock()

	rn.mu.Lock()
	delete(rn.active, id)
	rn.mu.Unlock()
}

// observe applies one engine event to the run's state, tells the watchers,
// and queues finished steps for storage. It runs on the engine's coordinator
// and never blocks: steps has room for every node.
//
// observe 把一个引擎事件应用到运行状态，通知观察者，并把已结束的步骤排队等待存储。它运行
// 在引擎的协调者上且从不阻塞：steps 能容纳每一个节点。
func (rn *studioRunner) observe(a *studioActiveRun, e flow.Event, steps chan<- StudioRunStep) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch e.Type {
	case flow.EventNodeStarted:
		a.running[e.Node] = &strings.Builder{}
		a.order = append(a.order, e.Node)
		a.publishLocked("node_started", struct {
			NodeID    string    `json:"node_id"`
			StartedAt time.Time `json:"started_at"`
		}{e.Node, e.Time})
	case flow.EventNodeDelta:
		if b, ok := a.running[e.Node]; ok && b.Len()+len(e.Delta) <= maxStudioStepJSON/2 {
			b.WriteString(e.Delta)
		}
		a.publishLocked("node_delta", struct {
			NodeID string `json:"node_id"`
			Delta  string `json:"delta"`
		}{e.Node, e.Delta})
	case flow.EventNodeFinished, flow.EventNodeSkipped:
		delete(a.running, e.Node)
		for i, id := range a.order {
			if id == e.Node {
				a.order = append(a.order[:i], a.order[i+1:]...)
				break
			}
		}
		step := storedStep(*e.Step)
		a.run.Steps = append(a.run.Steps, step)
		a.publishLocked(string(e.Type), renderRunStep(step))
		steps <- step
	}
}

// storedStep is an engine step in the form it is stored: values cut to the
// control plane's bounds.
//
// storedStep 是以存储形式表示的引擎步骤：取值截断到控制面的上限内。
func storedStep(s flow.Step) StudioRunStep {
	return StudioRunStep{
		NodeID: s.Node, NodeType: string(s.Type), Status: string(s.Status), ErrorCode: s.Error, Model: s.Model,
		Inputs: fitValues(s.Inputs, maxStudioStepJSON), Outputs: fitValues(s.Outputs, maxStudioStepJSON),
		PromptTokens: s.Usage.PromptTokens, CompletionTokens: s.Usage.CompletionTokens, StartedAt: s.StartedAt, FinishedAt: s.FinishedAt,
	}
}

// fitValues returns values with its longest values cut until the whole
// encodes within limit. A cut value ends in "…".
//
// fitValues 返回 values 的副本，并截短其中最长的取值，直到整体编码后不超过 limit。被截断的
// 取值以「…」结尾。
func fitValues(values map[string]string, limit int) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		out[k] = v
	}
	for {
		body, _ := json.Marshal(out)
		if len(body) <= limit {
			return out
		}
		longest := ""
		for k, v := range out {
			if len(v) > len(out[longest]) || longest == "" {
				longest = k
			}
		}
		v := out[longest]
		if v == "" || v == "…" {
			return out // only keys remain; the control plane will refuse it
		}
		cut := len(v) / 2
		for cut > 0 && !utf8.RuneStart(v[cut]) {
			cut--
		}
		out[longest] = strings.TrimSuffix(v[:cut], "…") + "…"
	}
}

// holdLease renews the run's lease until renewed is closed. A refusal means
// the run is no longer this replica's; so does failing to renew for a whole
// lease, since the control plane has by then let it lapse.
//
// holdLease 持续续租，直到 renewed 被关闭。被拒绝意味着该运行已不属于本副本；一整个租约
// 期间都续租失败也是如此，因为届时控制面已让它失效。
func (rn *studioRunner) holdLease(ctx context.Context, owner StudioOwner, id string, cancel context.CancelCauseFunc, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	lastOK := rn.h.clock.Now()
	for {
		tick, stopTimer := rn.h.clock.NewTimer(studioRunRenewEvery)
		select {
		case <-stop:
			stopTimer()
			return
		case <-tick:
		}
		err := rn.store.RenewRun(ctx, owner, id, rn.replica, studioRunLease)
		switch {
		case err == nil:
			lastOK = rn.h.clock.Now()
		case errors.Is(err, ErrStudioRunFinished), errors.Is(err, ErrStudioNotFound):
			cancel(errStudioRunLost)
		default:
			rn.logger.Warn("studio run lease was not renewed", slog.String("run_id", id), slog.Any("error", err))
			if rn.h.clock.Now().Sub(lastOK) >= studioRunLease {
				cancel(errStudioRunLost)
			}
		}
	}
}

// writeSteps stores each finished step in order, with bounded retries. It
// never blocks the run; a step that cannot be written is logged and the run
// goes on, but a refusal means the run is no longer this replica's.
//
// writeSteps 按顺序以有界重试保存每个已结束的步骤。它从不阻塞运行；写不进去的步骤记录日志
// 后运行继续，但被拒绝意味着该运行已不属于本副本。
func (rn *studioRunner) writeSteps(ctx context.Context, owner StudioOwner, id string, cancel context.CancelCauseFunc, steps <-chan StudioRunStep, done chan<- struct{}) {
	defer close(done)
	lost := false
	for step := range steps {
		if lost {
			continue
		}
		var err error
		for attempt := range studioStepWriteTries {
			if attempt > 0 && !rn.pause(time.Duration(attempt)*200*time.Millisecond) {
				break
			}
			if err = rn.store.RecordStep(ctx, owner, id, rn.replica, step); err == nil || errors.Is(err, ErrStudioInvalid) {
				break
			}
			if errors.Is(err, ErrStudioRunFinished) || errors.Is(err, ErrStudioNotFound) {
				lost = true
				cancel(errStudioRunLost)
				break
			}
		}
		if err != nil && !lost {
			rn.logger.Warn("studio run step was not saved", slog.String("run_id", id), slog.String("node_id", step.NodeID), slog.Any("error", err))
		}
	}
}

// finish stores how the run ended, retrying until it is stored, refused, or
// the lease it was held under has surely lapsed.
//
// finish 保存运行的结束方式，重试直到保存成功、被拒绝，或其所依据的租约必然已经失效。
func (rn *studioRunner) finish(ctx context.Context, owner StudioOwner, id string, result flow.Result) {
	finish := StudioRunFinish{
		Status: string(result.Status), ErrorCode: result.Error, FailedNode: result.FailedNode,
		Outputs: fitValues(result.Outputs, maxStudioRunOutputJSON), PromptTokens: result.Usage.PromptTokens, CompletionTokens: result.Usage.CompletionTokens,
	}
	deadline := rn.h.clock.Now().Add(studioRunLease)
	for {
		err := rn.store.FinishRun(ctx, owner, id, rn.replica, finish)
		if err == nil || errors.Is(err, ErrStudioRunFinished) || errors.Is(err, ErrStudioNotFound) || errors.Is(err, ErrStudioInvalid) {
			if err != nil {
				rn.logger.Warn("studio run end was refused", slog.String("run_id", id), slog.Any("error", err))
			}
			return
		}
		if rn.h.clock.Now().After(deadline) || !rn.pause(studioFinishRetryWait) {
			rn.logger.Error("studio run end was not saved", slog.String("run_id", id), slog.Any("error", err))
			return
		}
	}
}

// pause waits d, or reports false when the runner is closing.
// pause 等待 d；执行器正在关闭时报告 false。
func (rn *studioRunner) pause(d time.Duration) bool {
	tick, stop := rn.h.clock.NewTimer(d)
	select {
	case <-tick:
		return true
	case <-rn.closing:
		stop()
		return false
	}
}
