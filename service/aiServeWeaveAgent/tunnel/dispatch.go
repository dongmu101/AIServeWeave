package tunnel

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
)

// This file turns a dispatched frame into a runtime call. It is the tunnel's
// only consumer of the runtime interfaces, and the slot pool's only Handler
// in production.
//
// Three rules shape everything here:
//
//   - The allowlist is the last line of defence. A runtime_id that the
//     operator never declared is refused even when the Manager holds it and
//     even when the control plane asked for it. A compromised replica must
//     not be able to turn this Agent into an internal port scanner.
//   - Capability gating is not repeated. The runtime adapters already call
//     CapabilitySet.Require on their own Discover results; the dispatcher's
//     only job is to carry the resulting RuntimeError across the tunnel
//     without losing its code, its retryable flag or its sentinel cause.
//   - Nothing is buffered without a bound. Stream events are forwarded one
//     frame at a time as they are produced, artifacts are read and sent in
//     fixed-size pieces, and the one request body that exists (a workflow
//     template) is capped.

// dispatchOperation is the Operation recorded on errors the dispatcher itself
// raises, so a rejection is attributable without a runtime.
const dispatchOperation = "tunnel_dispatch"

// Local request limits, from README.md's `limits` configuration section.
const (
	defaultMaxDeadline     = 30 * time.Minute
	defaultMaxFrameBytes   = 4 << 20
	defaultMaxRequestBytes = 64 << 20
)

// DispatchConfig configures the request dispatcher.
type DispatchConfig struct {
	// Manager owns the local runtime instances. It is required.
	Manager runtime.Manager
	// NodeID labels the dispatcher's metrics. The limiter is a node-wide
	// quota shared by every replica, so its rejections are attributed to the
	// node and to the runtime instance, never to a replica.
	NodeID string
	// AllowedRuntimes is the local allowlist. An empty list means the caller
	// applied no narrowing, so main.go must pass every id in the runtimes
	// section for the README's default to hold; anything outside the list is
	// refused whoever asked for it.
	AllowedRuntimes []string
	// MaxDeadline caps the deadline a replica may impose. A longer one is
	// truncated to this value rather than rejected: the request is
	// answerable, just not for as long as the replica hoped. Default 30m.
	MaxDeadline time.Duration
	// MaxFrameBytes bounds a single response frame and is also the artifact
	// read size. Default 4Mi.
	MaxFrameBytes int
	// MaxRequestBytes bounds a whole request body — in practice a workflow
	// template. Default 64Mi.
	MaxRequestBytes int
	// Admission is the optional node-wide admission gate (resource pressure
	// plus per-class semaphores) applied on top of each runtime's own
	// limiter. Nil admits everything the limiter admits.
	//
	// Admission 是可选的整机准入闸门（资源紧张检查加分类信号量），叠加在每个运行时
	// 自身的限流器之上。为 nil 时，限流器放行的请求全部放行。
	Admission Admitter
	// Metrics receives the limiter instrument. Nil discards it.
	Metrics runtime.Metrics
	// Clock defaults to runtime.NewSystemClock; the deadline arithmetic runs
	// on it so tests need no real time.
	Clock runtime.Clock
	// Logger defaults to slog.Default.
	Logger *slog.Logger
}

// Dispatcher executes the requests a Gateway replica dispatches into this
// node's slots. It implements Handler.
type Dispatcher struct {
	cfg     DispatchConfig
	clock   runtime.Clock
	logger  *slog.Logger
	metrics *recorder

	// mu guards limiters, the per-instance concurrency gates.
	mu       sync.Mutex
	limiters map[string]*limiterEntry
}

// limiterEntry is one runtime instance's concurrency gate, tagged with the
// descriptor it was built for. Descriptor is comparable, so a Replace that
// changes the instance's address or its MaxConcurrent produces a fresh
// limiter while the old one keeps draining the requests it already granted.
type limiterEntry struct {
	desc    runtime.Descriptor
	limiter *runtime.Limiter
}

// NewDispatcher validates cfg and returns a Handler for the slot pool.
func NewDispatcher(cfg DispatchConfig) (*Dispatcher, error) {
	if cfg.Manager == nil {
		return nil, &runtime.RuntimeError{
			Code:      runtime.ErrorInvalidConfig,
			Operation: dispatchOperation,
			Message:   "invalid dispatcher configuration: manager is required",
			Cause:     ErrFatal,
		}
	}
	setDefaultDuration(&cfg.MaxDeadline, defaultMaxDeadline)
	setDefaultInt(&cfg.MaxFrameBytes, defaultMaxFrameBytes)
	setDefaultInt(&cfg.MaxRequestBytes, defaultMaxRequestBytes)
	if cfg.Clock == nil {
		cfg.Clock = runtime.NewSystemClock()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	cfg.AllowedRuntimes = slices.Clone(cfg.AllowedRuntimes)

	return &Dispatcher{
		cfg:      cfg,
		clock:    cfg.Clock,
		logger:   cfg.Logger,
		metrics:  newRecorder(cfg.Metrics, cfg.NodeID, ""),
		limiters: map[string]*limiterEntry{},
	}, nil
}

// Handle implements Handler: it validates the request, reserves quota and
// runs the operation, returning the error that becomes ResponseEnd.
func (d *Dispatcher) Handle(ctx context.Context, req *Request, sink ResponseSink) error {
	headers := req.Headers
	spec, err := tunnelwire.SpecFor(headers.GetOperation())
	if err != nil {
		return err
	}

	id := headers.GetRuntimeId()
	if !d.allowed(id) {
		// Deliberately the same answer whether the id is unknown or merely
		// forbidden: a caller probing which runtimes exist learns nothing.
		return &runtime.RuntimeError{
			Code:      runtime.ErrorInvalidConfig,
			RuntimeID: id,
			Operation: dispatchOperation,
			Message:   "runtime is not in this agent's allowlist",
		}
	}

	rt, ok := d.cfg.Manager.Get(id)
	if !ok {
		d.forget(id)
		return &runtime.RuntimeError{
			Code:      runtime.ErrorInvalidConfig,
			RuntimeID: id,
			Operation: dispatchOperation,
			Message:   "no such runtime on this node",
			Cause:     runtime.ErrRuntimeNotFound,
		}
	}

	ctx, cancel, err := d.withDeadline(ctx, id, headers.GetDeadlineUnixMs())
	if err != nil {
		return err
	}
	defer cancel()

	// Node-wide admission runs in cheapest-refusal-first order: the pressure
	// gate never blocks, the per-runtime limiter never blocks, and only then
	// may the class semaphore wait, so a request never waits just to be
	// turned away by a check that could have answered at once.
	//
	// 整机准入按「最便宜的拒绝在前」排序：资源闸门从不阻塞，按运行时的限流器从不阻塞，
	// 之后分类信号量才可能等待，这样请求不会先等一阵、再被一个本可以立刻回答的检查拒绝。
	gate, class := admissionFor(spec.Operation, rt.Descriptor().Kind)
	if gate && d.cfg.Admission != nil {
		if err := d.cfg.Admission.CheckPressure(admissionLabel(class)); err != nil {
			return withRuntimeID(err, id)
		}
	}

	// The quota is released by this defer on every path there is: a normal
	// return, an early error, a cancelled context, and a panic unwinding
	// through the slot's recover. Streaming operations hold it for the whole
	// stream, matching runtime/limiter.go's existing semantics.
	release, err := d.acquire(rt)
	if err != nil {
		return err
	}
	defer release()

	if class != "" && d.cfg.Admission != nil {
		admitted, err := d.cfg.Admission.Acquire(ctx, class)
		if err != nil {
			return withRuntimeID(err, id)
		}
		defer admitted()
	}

	return d.run(ctx, rt, spec, req, sink)
}

// run performs one operation against rt. It logs one start and one finish
// event keyed by req.ID, covering every operation through this single entry
// point rather than one log call per case below.
//
// run 对 rt 执行一次操作。它以 req.ID 为键记一条起始与一条结束事件，通过这一个
// 统一入口覆盖全部操作，而不是在下面每个 case 里各写一次日志调用。
func (d *Dispatcher) run(ctx context.Context, rt runtime.Runtime, spec tunnelwire.OperationSpec, req *Request, sink ResponseSink) (err error) {
	started := d.clock.Now()
	runtimeID := req.Headers.GetRuntimeId()
	d.logger.Info("backend call started",
		slog.String("request_id", req.ID),
		slog.String("runtime_id", runtimeID),
		slog.String("operation", spec.Operation.String()))
	defer func() {
		d.logger.Info("backend call finished",
			slog.String("request_id", req.ID),
			slog.String("runtime_id", runtimeID),
			slog.String("operation", spec.Operation.String()),
			slog.Bool("success", err == nil),
			slog.Duration("duration", d.clock.Now().Sub(started)))
	}()

	id := req.Headers.GetRuntimeId()
	reqPayload := req.Headers.GetPayload()

	switch spec.Operation {
	case tunnelv1.Operation_OPERATION_LIST_MODELS:
		ir, err := inferenceRuntime(rt, id)
		if err != nil {
			return err
		}
		models, err := ir.ListModels(ctx)
		if err != nil {
			return err
		}
		payload, err := tunnelwire.MarshalModels(models)
		if err != nil {
			return err
		}
		return d.sendChunk(sink, payload)

	case tunnelv1.Operation_OPERATION_CHAT:
		ir, err := inferenceRuntime(rt, id)
		if err != nil {
			return err
		}
		chatReq, err := tunnelwire.UnmarshalChatRequest(reqPayload)
		if err != nil {
			return err
		}
		resp, err := ir.Chat(ctx, chatReq)
		if err != nil {
			return err
		}
		payload, err := tunnelwire.MarshalChatResponse(resp)
		if err != nil {
			return err
		}
		return d.sendChunk(sink, payload)

	case tunnelv1.Operation_OPERATION_CHAT_STREAM:
		ir, err := inferenceRuntime(rt, id)
		if err != nil {
			return err
		}
		chatReq, err := tunnelwire.UnmarshalChatRequest(reqPayload)
		if err != nil {
			return err
		}
		stream, err := ir.ChatStream(ctx, chatReq)
		if err != nil {
			return err
		}
		return forwardStream(sink, stream, tunnelwire.MarshalChatEvent, d.cfg.MaxFrameBytes)

	case tunnelv1.Operation_OPERATION_MESSAGES:
		mr, ok := rt.(runtime.MessagesRuntime)
		if !ok {
			return &runtime.RuntimeError{Code: runtime.ErrorCapability, RuntimeID: id, Message: "native Messages capability unavailable"}
		}
		messageReq, err := tunnelwire.UnmarshalMessagesRequest(reqPayload)
		if err != nil {
			return err
		}
		stream, err := mr.Messages(ctx, messageReq)
		if err != nil {
			return err
		}
		return forwardStream(sink, stream, tunnelwire.MarshalMessagesEvent, d.messagesFrameLimit())

	case tunnelv1.Operation_OPERATION_IMAGE_GENERATE:
		ir, ok := rt.(runtime.ImageRuntime)
		if !ok {
			return &runtime.RuntimeError{Code: runtime.ErrorCapability, RuntimeID: id, Message: "image generation capability unavailable", Cause: runtime.ErrCapabilityUnsupported}
		}
		imageReq, err := tunnelwire.UnmarshalImageRequest(reqPayload)
		if err != nil {
			return err
		}
		stream, err := ir.GenerateImage(ctx, imageReq)
		if err != nil {
			return err
		}
		return forwardStream(sink, stream, tunnelwire.MarshalImageEvent, d.cfg.MaxFrameBytes)

	case tunnelv1.Operation_OPERATION_EMBED:
		ir, err := inferenceRuntime(rt, id)
		if err != nil {
			return err
		}
		embedReq, err := tunnelwire.UnmarshalEmbeddingRequest(reqPayload)
		if err != nil {
			return err
		}
		resp, err := ir.Embed(ctx, embedReq)
		if err != nil {
			return err
		}
		payload, err := tunnelwire.MarshalEmbeddingResponse(resp)
		if err != nil {
			return err
		}
		return d.sendChunk(sink, payload)

	case tunnelv1.Operation_OPERATION_RERANK:
		ir, err := inferenceRuntime(rt, id)
		if err != nil {
			return err
		}
		rerankReq, err := tunnelwire.UnmarshalRerankRequest(reqPayload)
		if err != nil {
			return err
		}
		resp, err := ir.Rerank(ctx, rerankReq)
		if err != nil {
			return err
		}
		payload, err := tunnelwire.MarshalRerankResponse(resp)
		if err != nil {
			return err
		}
		return d.sendChunk(sink, payload)

	case tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT:
		wr, err := workflowRuntime(rt, id)
		if err != nil {
			return err
		}
		wfReq, err := tunnelwire.UnmarshalWorkflowRequest(reqPayload)
		if err != nil {
			return err
		}
		// The template travels as DataChunks, never inside RequestHeaders:
		// it is the one payload big enough to threaten the frame limit.
		template, err := d.readBody(ctx, id, req.Body)
		if err != nil {
			return err
		}
		if len(template) == 0 {
			return &runtime.RuntimeError{
				Code:      runtime.ErrorProtocol,
				RuntimeID: id,
				Operation: dispatchOperation,
				Message:   "workflow submission carried no template",
			}
		}
		wfReq.Template = template
		run, err := wr.Submit(ctx, wfReq)
		if err != nil {
			return err
		}
		payload, err := tunnelwire.MarshalWorkflowRun(run)
		if err != nil {
			return err
		}
		return d.sendChunk(sink, payload)

	case tunnelv1.Operation_OPERATION_WORKFLOW_SUBSCRIBE:
		wr, err := workflowRuntime(rt, id)
		if err != nil {
			return err
		}
		runID, err := tunnelwire.UnmarshalRunRef(reqPayload)
		if err != nil {
			return err
		}
		stream, err := wr.Subscribe(ctx, runID)
		if err != nil {
			return err
		}
		return forwardStream(sink, stream, tunnelwire.MarshalWorkflowEvent, d.cfg.MaxFrameBytes)

	case tunnelv1.Operation_OPERATION_WORKFLOW_STATUS:
		wr, err := workflowRuntime(rt, id)
		if err != nil {
			return err
		}
		runID, err := tunnelwire.UnmarshalRunRef(reqPayload)
		if err != nil {
			return err
		}
		status, err := wr.Status(ctx, runID)
		if err != nil {
			return err
		}
		payload, err := tunnelwire.MarshalWorkflowStatus(status)
		if err != nil {
			return err
		}
		return d.sendChunk(sink, payload)

	case tunnelv1.Operation_OPERATION_WORKFLOW_CANCEL:
		wr, err := workflowRuntime(rt, id)
		if err != nil {
			return err
		}
		runID, err := tunnelwire.UnmarshalRunRef(reqPayload)
		if err != nil {
			return err
		}
		// The only operation whose success is a bare ResponseEnd.
		return wr.Cancel(ctx, runID)

	case tunnelv1.Operation_OPERATION_ARTIFACT_LIST:
		wr, err := workflowRuntime(rt, id)
		if err != nil {
			return err
		}
		runID, err := tunnelwire.UnmarshalRunRef(reqPayload)
		if err != nil {
			return err
		}
		refs, err := wr.Artifacts(ctx, runID)
		if err != nil {
			return err
		}
		// An empty list is still sent as a chunk: "this run produced nothing"
		// is an answer, and the caller must be able to tell it apart from a
		// reply that never arrived.
		//
		// 空列表同样作为一个块发出：「这次运行什么都没产出」是一个答复，调用方必须能
		// 把它与「答复根本没到」区分开。
		payload, err := tunnelwire.MarshalArtifactList(refs)
		if err != nil {
			return err
		}
		return d.sendChunk(sink, payload)

	case tunnelv1.Operation_OPERATION_ARTIFACT_OPEN:
		wr, err := workflowRuntime(rt, id)
		if err != nil {
			return err
		}
		ref, err := tunnelwire.UnmarshalArtifactRef(reqPayload)
		if err != nil {
			return err
		}
		artifact, err := wr.OpenArtifact(ctx, ref)
		if err != nil {
			return err
		}
		return d.forwardArtifact(ctx, sink, id, artifact)

	case tunnelv1.Operation_OPERATION_INPUT_UPLOAD:
		wr, err := workflowRuntime(rt, id)
		if err != nil {
			return err
		}
		meta, err := tunnelwire.UnmarshalInputUploadRequest(reqPayload)
		if err != nil {
			return err
		}
		if meta.Filename == "" {
			return &runtime.RuntimeError{
				Code:      runtime.ErrorProtocol,
				RuntimeID: id,
				Operation: dispatchOperation,
				Message:   "input upload carried no filename",
			}
		}
		// The file streams straight from the DataChunk channel into
		// UploadInput rather than being collected first like readBody does
		// for a workflow template: an input file has no size guarantee
		// remotely as tight as a template's, and buffering it whole here
		// would be exactly the unbounded hold README.md's dispatch.go rule
		// forbids.
		//
		// 文件直接从 DataChunk 通道流进 UploadInput，而不是像 readBody 对
		// 工作流模板那样先收集完整——输入文件远没有模板那样紧的体积保证，
		// 在这里把它整体缓冲下来，正是 README.md 对 dispatch.go 的规则所
		// 禁止的那种无界持有。
		body := &chanReader{ctx: ctx, body: req.Body, limit: d.cfg.MaxRequestBytes, id: id}
		result, err := wr.UploadInput(ctx, meta, body)
		if err != nil {
			return err
		}
		payload, err := tunnelwire.MarshalInputUploadResult(result)
		if err != nil {
			return err
		}
		return d.sendChunk(sink, payload)

	case tunnelv1.Operation_OPERATION_AUDIO_TRANSCRIBE:
		ir, err := inferenceRuntime(rt, id)
		if err != nil {
			return err
		}
		audioReq, err := tunnelwire.UnmarshalAudioTranscriptionRequest(reqPayload)
		if err != nil {
			return err
		}
		if audioReq.Model == "" {
			return &runtime.RuntimeError{
				Code:      runtime.ErrorProtocol,
				RuntimeID: id,
				Operation: dispatchOperation,
				Message:   "audio transcription carried no model",
			}
		}
		// The audio streams straight from the DataChunk channel into
		// Transcribe, the same discipline OPERATION_INPUT_UPLOAD applies
		// just above: an audio file has no tight size guarantee, so
		// buffering it whole here would be the unbounded hold README.md's
		// dispatch.go rule forbids.
		//
		// 音频直接从 DataChunk 通道流进 Transcribe，与上面
		// OPERATION_INPUT_UPLOAD 遵循的纪律相同：音频文件没有紧的体积
		// 保证，在这里把它整体缓冲下来正是 README.md 对 dispatch.go 的
		// 规则所禁止的那种无界持有。
		audio := &chanReader{ctx: ctx, body: req.Body, limit: d.cfg.MaxRequestBytes, id: id}
		result, err := ir.Transcribe(ctx, audioReq, audio)
		if err != nil {
			return err
		}
		payload, err := tunnelwire.MarshalAudioTranscriptionResponse(result)
		if err != nil {
			return err
		}
		return d.sendChunk(sink, payload)

	default:
		// tunnelwire.SpecFor already rejected unknown operations, so reaching here means
		// the table and this switch have drifted apart.
		return &runtime.RuntimeError{
			Code:      runtime.ErrorProtocol,
			RuntimeID: id,
			Operation: dispatchOperation,
			Message:   "operation " + tunnelwire.OperationName(spec.Operation) + " has no dispatch path",
		}
	}
}

// -----------------------------------------------------------------------
// Gating
// -----------------------------------------------------------------------

// allowed reports whether id may be dispatched to.
func (d *Dispatcher) allowed(id string) bool {
	if id == "" {
		return false
	}
	if len(d.cfg.AllowedRuntimes) == 0 {
		return true
	}
	return slices.Contains(d.cfg.AllowedRuntimes, id)
}

// inferenceRuntime and workflowRuntime narrow a Runtime to the interface an
// operation needs. A backend that does not implement it is a capability gap,
// not a protocol error: the replica should schedule the request elsewhere.
func inferenceRuntime(rt runtime.Runtime, id string) (runtime.InferenceRuntime, error) {
	ir, ok := rt.(runtime.InferenceRuntime)
	if !ok {
		return nil, capabilityGap(rt, id, "inference")
	}
	return ir, nil
}

func workflowRuntime(rt runtime.Runtime, id string) (runtime.WorkflowRuntime, error) {
	wr, ok := rt.(runtime.WorkflowRuntime)
	if !ok {
		return nil, capabilityGap(rt, id, "workflow")
	}
	return wr, nil
}

func capabilityGap(rt runtime.Runtime, id, want string) error {
	return &runtime.RuntimeError{
		Code:      runtime.ErrorCapability,
		RuntimeID: id,
		Kind:      rt.Descriptor().Kind,
		Operation: dispatchOperation,
		Message:   "runtime does not provide " + want + " operations",
		Cause:     runtime.ErrCapabilityUnsupported,
	}
}

// withDeadline applies the smaller of the replica's deadline and the local
// maximum. The deadline is carried in-band because a reused slot outlives any
// one request, so the gRPC stream deadline cannot express a per-request one.
//
// The budget is computed against the injected Clock but installed as a
// timeout rather than an absolute deadline: a context timer always runs on
// real time, so handing it an instant derived from a test clock would expire
// the request before it started. The Clock decides how long the request may
// take; the runtime's own timer enforces it.
func (d *Dispatcher) withDeadline(ctx context.Context, id string, deadlineUnixMs int64) (context.Context, context.CancelFunc, error) {
	budget := d.cfg.MaxDeadline

	if deadlineUnixMs > 0 {
		remaining := time.UnixMilli(deadlineUnixMs).Sub(d.clock.Now())
		if remaining <= 0 {
			// Already expired in flight: answering costs the backend a whole
			// request whose result nobody is waiting for.
			return nil, nil, &runtime.RuntimeError{
				Code:      runtime.ErrorTimeout,
				RuntimeID: id,
				Operation: dispatchOperation,
				Message:   "the request deadline had already passed when it arrived",
				Cause:     context.DeadlineExceeded,
			}
		}
		budget = min(budget, remaining)
	}

	ctx, cancel := context.WithTimeout(ctx, budget)
	return ctx, cancel, nil
}

// acquire reserves this instance's concurrency quota, without ever blocking:
// at capacity the caller gets ErrorBackpressure and the replica moves the
// request to another node. The limiter is the node's hard quota — the slot
// watermarks are only a soft one — so this is where genuine overload is
// refused.
func (d *Dispatcher) acquire(rt runtime.Runtime) (func(), error) {
	desc := rt.Descriptor()

	d.mu.Lock()
	entry, ok := d.limiters[desc.ID]
	if !ok || entry.desc != desc {
		entry = &limiterEntry{desc: desc, limiter: runtime.NewLimiter(desc.MaxConcurrent)}
		d.limiters[desc.ID] = entry
	}
	limiter := entry.limiter
	d.mu.Unlock()

	release, err := limiter.Acquire()
	if err != nil {
		// The soft slot quota let this request in and the hard quota turned
		// it away. That is the designed overshoot, and this counter is how
		// node_total gets calibrated against it.
		d.metrics.LimiterRejection(desc.ID)
	}
	return release, err
}

// Admitter is the node-wide admission gate the Agent's admission package
// implements. It is declared here so this package does not import that one.
//
// Admitter 是 Agent 的 admission 包实现的整机准入闸门。在这里声明，使本包不必 import
// 那个包。
type Admitter interface {
	// CheckPressure refuses at once while the machine is under resource
	// pressure; it never blocks.
	CheckPressure(class string) error
	// Acquire reserves one slot of the class semaphore, possibly waiting a
	// bounded time. release must be called exactly once.
	Acquire(ctx context.Context, class string) (release func(), err error)
}

// Admission classes, matching the admission package's constants.
const (
	admissionClassCLI     = "cli"
	admissionClassGeneral = "general"
	// admissionLabelWorkflow labels gate-only refusals of operations that
	// take no class slot.
	admissionLabelWorkflow = "workflow"
)

// admissionFor decides how node-wide admission applies to one operation on
// one runtime kind. gate means the resource-pressure check applies; class,
// when non-empty, names the semaphore the request holds for its whole
// duration.
//
//   - Hosted providers are exempt: their compute is remote, so the local
//     machine is not what they load.
//   - Status, cancel, subscribe and artifact reads are exempt, so a
//     saturated node still answers for work it already accepted.
//   - Workflow submission and input upload pass the gate but take no slot:
//     a submit holds for milliseconds and the GPU work then runs in
//     ComfyUI's own queue, so a slot would bound nothing.
//   - Everything else takes the cli slot on a local CLI runtime (a process
//     per request) and the general slot on other local backends.
//
// admissionFor 决定整机准入如何作用于某个运行时类型上的某个操作。gate 表示适用资源紧张
// 检查；class 非空时指明请求在整个执行期间占用的信号量。
//
//   - 托管供应商豁免：它们的算力在远端，加重的不是本机。
//   - 状态、取消、订阅与产物读取豁免，让满载的节点仍能回应已经接下的工作。
//   - 工作流提交与输入上传过闸门但不占位置：提交只持续几毫秒，GPU 工作随后在 ComfyUI
//     自己的队列里执行，占位置约束不了任何东西。
//   - 其余操作在本地 CLI 运行时（每请求一个进程）上占 cli 位置，在其他本地后端上占
//     general 位置。
func admissionFor(op tunnelv1.Operation, kind runtime.Kind) (gate bool, class string) {
	if kind.IsHosted() {
		return false, ""
	}
	switch op {
	case tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT, tunnelv1.Operation_OPERATION_INPUT_UPLOAD:
		return true, ""
	case tunnelv1.Operation_OPERATION_CHAT, tunnelv1.Operation_OPERATION_CHAT_STREAM,
		tunnelv1.Operation_OPERATION_MESSAGES, tunnelv1.Operation_OPERATION_IMAGE_GENERATE,
		tunnelv1.Operation_OPERATION_EMBED, tunnelv1.Operation_OPERATION_RERANK,
		tunnelv1.Operation_OPERATION_AUDIO_TRANSCRIBE:
		if kind.IsLocalCLI() {
			return true, admissionClassCLI
		}
		return true, admissionClassGeneral
	default:
		return false, ""
	}
}

func admissionLabel(class string) string {
	if class == "" {
		return admissionLabelWorkflow
	}
	return class
}

// withRuntimeID tags an admission refusal with the runtime it was for, so
// the Gateway's error carries the same context as a limiter refusal.
func withRuntimeID(err error, id string) error {
	var rtErr *runtime.RuntimeError
	if errors.As(err, &rtErr) && rtErr.RuntimeID == "" {
		tagged := *rtErr
		tagged.RuntimeID = id
		return &tagged
	}
	return err
}

// forget drops the limiter of a runtime the Manager no longer has, so a node
// that is reconfigured repeatedly does not accumulate dead gates.
func (d *Dispatcher) forget(id string) {
	d.mu.Lock()
	delete(d.limiters, id)
	d.mu.Unlock()
}

// -----------------------------------------------------------------------
// Response framing
// -----------------------------------------------------------------------

// sendChunk writes one DataChunk after checking it against the frame limit.
func (d *Dispatcher) sendChunk(sink ResponseSink, payload []byte) error {
	return sendFrame(sink, payload, d.cfg.MaxFrameBytes)
}

// messagesFrameLimit bounds one Messages event frame. The tunnelwire codec
// already caps the event at the Messages API's own size, so the frame may use
// the whole gRPC message both tunnel ends accept; the Gateway applies the same.
// messagesFrameLimit 限制单个 Messages 事件帧。tunnelwire 编解码器已把事件限制在
// Messages API 自身的大小，因此帧可以用满隧道两端都接受的整条 gRPC 消息；Gateway 同样如此。
func (d *Dispatcher) messagesFrameLimit() int {
	return max(d.cfg.MaxFrameBytes, tunnelwire.MaxTunnelMessageBytes)
}

func sendFrame(sink ResponseSink, payload []byte, limit int) error {
	if limit > 0 && len(payload) > limit {
		return &runtime.RuntimeError{
			Code:      runtime.ErrorResponseTooLarge,
			Operation: dispatchOperation,
			Message: "a response frame of " + strconv.Itoa(len(payload)) +
				" bytes exceeds the local limit of " + strconv.Itoa(limit),
		}
	}
	return sink.Data(payload)
}

// forwardStream sends one frame per event, the moment the event is produced.
// This is the single guarantee behind the tunnel's time-to-first-token: any
// "collect N events and send them together" optimisation has to prove it does
// not delay the first one, and none can.
func forwardStream[T any](sink ResponseSink, stream runtime.Stream[T], marshal func(T) ([]byte, error), limit int) error {
	defer func() { _ = stream.Close() }()

	for {
		event, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return committedError(err, stream.Committed())
		}

		payload, err := marshal(event)
		if err != nil {
			return committedError(err, stream.Committed())
		}
		if err := sendFrame(sink, payload, limit); err != nil {
			return committedError(err, stream.Committed())
		}
	}
}

// artifactUnknownSizeBuffer is the read buffer for an artifact whose length is
// not declared: big enough that a typical image or short clip needs few
// frames, small enough that an unknown-size small file does not pin a whole
// MaxFrameBytes buffer. A longer body simply takes more frames.
//
// artifactUnknownSizeBuffer 是长度未声明的产物所用的读缓冲：足够大，典型图片或
// 短视频不会拆出太多帧；又足够小，不会让一个未知大小的小文件占住整块
// MaxFrameBytes 缓冲。更长的 body 只是多用几帧。
const artifactUnknownSizeBuffer = 256 << 10

// artifactBufferSize picks the read buffer length for an artifact: the
// declared size when it is known (an image of a few dozen KB must not cost a
// 4MiB allocation), artifactUnknownSizeBuffer when it is not, and never more
// than maxFrame. maxFrame stays the hard bound on a frame, so this only ever
// makes frames smaller than before, never larger.
//
// artifactBufferSize 为产物选择读缓冲长度：大小已知时取声明大小（几十 KB 的图片
// 不该花掉 4MiB 的分配），未知时取 artifactUnknownSizeBuffer，且绝不超过
// maxFrame。maxFrame 仍是单帧的硬上限，因此这里只会让帧比以前更小，不会更大。
func artifactBufferSize(size int64, maxFrame int) int {
	want := artifactUnknownSizeBuffer
	if size > 0 {
		want = int(min(size, int64(maxFrame)))
	}
	return max(min(want, maxFrame), 1)
}

// Artifact read buffers are recycled across requests in power-of-four size
// classes (16KiB, 64KiB, ..., 64MiB). Classes keep a buffer taken for a small
// file from being handed to a large one: getArtifactBuffer returns a buffer of
// the exact length asked for, cut from a class that is at least that big, so
// the frame size is determined by artifactBufferSize and not by whichever
// buffer happened to be in the pool. A request larger than the top class
// allocates directly and is not pooled.
//
// 产物读缓冲按 4 的幂次大小分档（16KiB、64KiB、……、64MiB）跨请求复用。分档避免
// 为小文件取出的缓冲被交给大文件：getArtifactBuffer 返回长度恰为所求的缓冲，
// 取自不小于该长度的档位，因此帧大小由 artifactBufferSize 决定，而不取决于池里
// 碰巧是哪块缓冲。超过最高档的请求直接分配，不进池。
const (
	artifactClassMinShift = 14
	artifactClassCount    = 7
)

var artifactBufferPools [artifactClassCount]sync.Pool

// artifactClass returns the smallest class holding n bytes, or -1 when n is
// larger than the top class.
func artifactClass(n int) int {
	for k := range artifactClassCount {
		if n <= 1<<(artifactClassMinShift+2*k) {
			return k
		}
	}
	return -1
}

// getArtifactBuffer returns a buffer of length n. pooled is non-nil when the
// buffer came from (and should go back to) a pool; hand it to
// putArtifactBuffer when the transfer ends.
func getArtifactBuffer(n int) (pooled *[]byte, buf []byte) {
	k := artifactClass(n)
	if k < 0 {
		return nil, make([]byte, n)
	}
	if v, ok := artifactBufferPools[k].Get().(*[]byte); ok {
		return v, (*v)[:n]
	}
	full := make([]byte, 1<<(artifactClassMinShift+2*k))
	return &full, full[:n]
}

// putArtifactBuffer returns a buffer taken from getArtifactBuffer. A nil
// pooled (an unpooled oversize buffer) is a no-op.
func putArtifactBuffer(pooled *[]byte) {
	if pooled == nil {
		return
	}
	artifactBufferPools[artifactClass(cap(*pooled))].Put(pooled)
}

// forwardArtifact streams an artifact body as ResponseHeaders followed by
// bounded DataChunks. The body is never read into memory whole: a 500MB
// artifact must cost one buffer, not one allocation of its own size, and a
// small artifact must not cost a full-frame buffer either (see
// artifactBufferSize).
//
// forwardArtifact 把产物 body 作为 ResponseHeaders 加若干有上限的 DataChunk 流式
// 转发。body 绝不整体读入内存：500MB 的产物只该花一块缓冲，而不是自身大小的一次
// 分配；小产物同样不该花掉一整帧大小的缓冲（见 artifactBufferSize）。
func (d *Dispatcher) forwardArtifact(ctx context.Context, sink ResponseSink, id string, artifact runtime.Artifact) error {
	if artifact.Body != nil {
		defer func() { _ = artifact.Body.Close() }()
	}

	if err := sink.Headers(artifact.ContentType, artifact.Size); err != nil {
		return err
	}
	if artifact.Body == nil {
		return nil
	}

	pooled, buf := getArtifactBuffer(artifactBufferSize(artifact.Size, d.cfg.MaxFrameBytes))
	defer putArtifactBuffer(pooled)
	committed := false
	for {
		if err := ctx.Err(); err != nil {
			return committedError(context.Cause(ctx), committed)
		}
		n, readErr := artifact.Body.Read(buf)
		if n > 0 {
			if err := d.sendChunk(sink, buf[:n]); err != nil {
				return committedError(err, committed)
			}
			committed = true
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return committedError(&runtime.RuntimeError{
				Code:      runtime.ErrorUpstream,
				RuntimeID: id,
				Operation: dispatchOperation,
				Message:   "reading the artifact body failed",
				Retryable: true,
				Cause:     readErr,
			}, committed)
		}
	}
}

// committedError clears the retryable flag once output has already reached
// the user. A stream that has emitted its first token cannot be replayed on
// another node: the client would see the beginning of one answer followed by
// the whole of another.
func committedError(err error, committed bool) error {
	if err == nil || !committed {
		return err
	}
	var re *runtime.RuntimeError
	if !errors.As(err, &re) || !re.Retryable {
		return err
	}
	clone := *re
	clone.Retryable = false
	return &clone
}

// readBody collects the request body under the local size limit. The limit is
// enforced as the chunks arrive rather than afterwards, so an oversized body
// is refused instead of being accumulated first and rejected second.
func (d *Dispatcher) readBody(ctx context.Context, id string, body <-chan []byte) ([]byte, error) {
	var collected []byte
	for {
		select {
		case chunk, ok := <-body:
			if !ok {
				return collected, nil
			}
			if d.cfg.MaxRequestBytes > 0 && len(collected)+len(chunk) > d.cfg.MaxRequestBytes {
				return nil, &runtime.RuntimeError{
					Code:      runtime.ErrorResponseTooLarge,
					RuntimeID: id,
					Operation: dispatchOperation,
					Message: "the request body exceeds the local limit of " +
						strconv.Itoa(d.cfg.MaxRequestBytes) + " bytes",
				}
			}
			collected = append(collected, chunk...)
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
}

// chanReader adapts a request body channel to io.Reader, so a handler can
// stream it straight into a backend call (UploadImage's multipart writer,
// for instance) without ever holding the whole body in this process the way
// readBody deliberately does for the one caller that actually wants that
// (a workflow template, already size-bounded well below what an input file
// may need). The limit is enforced as chunks arrive, the same discipline
// readBody uses, so an oversized upload is cut off rather than accumulated
// first and rejected second — except here "accumulated" would mean written
// most of the way into a downstream request already in flight.
//
// chanReader 把一个请求体通道适配成 io.Reader，好让处理器能把它直接流进某次
// 后端调用（比如 UploadImage 的 multipart writer），而不必像 readBody 对
// 唯一真正需要那样做的调用方（工作流模板，其大小早已被限定在一个远比输入
// 文件可能需要的更低的上限）那样，把整个 body 持有在本进程里。限制在分片
// 到达时就地强制执行，与 readBody 相同的纪律，因此一次超大的上传会被就地
// 切断，而不是先攒够整个 body 才被拒绝——只是这里的「攒够」意味着已经把
// 大半个文件写进了一个仍在进行中的下游请求。
type chanReader struct {
	ctx     context.Context
	body    <-chan []byte
	limit   int
	read    int
	pending []byte
	id      string
}

func (r *chanReader) Read(p []byte) (int, error) {
	for len(r.pending) == 0 {
		select {
		case chunk, ok := <-r.body:
			if !ok {
				return 0, io.EOF
			}
			r.read += len(chunk)
			if r.limit > 0 && r.read > r.limit {
				return 0, &runtime.RuntimeError{
					Code:      runtime.ErrorResponseTooLarge,
					RuntimeID: r.id,
					Operation: dispatchOperation,
					Message: "the request body exceeds the local limit of " +
						strconv.Itoa(r.limit) + " bytes",
				}
			}
			r.pending = chunk
		case <-r.ctx.Done():
			return 0, context.Cause(r.ctx)
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}
