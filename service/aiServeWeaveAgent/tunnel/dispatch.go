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

	// The quota is released by this defer on every path there is: a normal
	// return, an early error, a cancelled context, and a panic unwinding
	// through the slot's recover. Streaming operations hold it for the whole
	// stream, matching runtime/limiter.go's existing semantics.
	release, err := d.acquire(rt)
	if err != nil {
		return err
	}
	defer release()

	return d.run(ctx, rt, spec, req, sink)
}

// run performs one operation against rt.
func (d *Dispatcher) run(ctx context.Context, rt runtime.Runtime, spec tunnelwire.OperationSpec, req *Request, sink ResponseSink) error {
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
		return forwardStream(d, sink, stream, tunnelwire.MarshalChatEvent)

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
		return forwardStream(d, sink, stream, tunnelwire.MarshalWorkflowEvent)

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
	if d.cfg.MaxFrameBytes > 0 && len(payload) > d.cfg.MaxFrameBytes {
		return &runtime.RuntimeError{
			Code:      runtime.ErrorResponseTooLarge,
			Operation: dispatchOperation,
			Message: "a response frame of " + strconv.Itoa(len(payload)) +
				" bytes exceeds the local limit of " + strconv.Itoa(d.cfg.MaxFrameBytes),
		}
	}
	return sink.Data(payload)
}

// forwardStream sends one frame per event, the moment the event is produced.
// This is the single guarantee behind the tunnel's time-to-first-token: any
// "collect N events and send them together" optimisation has to prove it does
// not delay the first one, and none can.
func forwardStream[T any](d *Dispatcher, sink ResponseSink, stream runtime.Stream[T], marshal func(T) ([]byte, error)) error {
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
		if err := d.sendChunk(sink, payload); err != nil {
			return committedError(err, stream.Committed())
		}
	}
}

// forwardArtifact streams an artifact body as ResponseHeaders followed by
// fixed-size DataChunks. The body is never read into memory whole: a 500MB
// artifact must cost one buffer, not one allocation of its own size.
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

	buf := make([]byte, d.cfg.MaxFrameBytes)
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
