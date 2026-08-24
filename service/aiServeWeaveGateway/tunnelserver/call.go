package tunnelserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"sync"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
)

// Request is one operation to run on a node. It is the raw dispatch form:
// payload bytes are already encoded per the operation's contract in
// common/tunnelwire. Callers that want Go types use NodeRuntime instead.
type Request struct {
	// NodeID names the node. It must be connected to this replica; there is
	// no forwarding to another replica.
	NodeID string
	// RuntimeID must be one the Agent allows. The Agent enforces its own
	// allowlist regardless of what is sent here.
	RuntimeID string
	Operation tunnelv1.Operation
	// Deadline is absolute and travels in-band, because a slot outlives the
	// request on it and so the gRPC stream deadline cannot express one. Zero
	// means the Agent applies only its local maximum.
	Deadline time.Time
	// Payload is the operation's request message, already marshalled.
	Payload []byte
	// Body streams a request that does not fit in Payload. Only workflow
	// submission uses it. The send function must not be called after Body
	// returns.
	Body func(send func([]byte) error) error
	// Trace carries a fixed key set (request_id, tenant_id) for correlation.
	// It never reaches a log on the Agent side and must not carry request
	// content.
	Trace map[string]string
	// Class overrides the slot class. Zero picks it from the operation:
	// artifact transfer goes to a bulk slot so a large download cannot
	// occupy an inference slot.
	Class tunnelv1.SlotClass
}

// Response is the frame-by-frame result of a dispatched request. Callers must
// call Close exactly once; leaving one unclosed strands the slot until the
// tunnel notices, which on a healthy link may be a long time.
type Response struct {
	ctx  context.Context
	call *call
	slot *slot

	headers  *tunnelv1.ResponseHeaders
	finished bool

	// The recording state below is touched only from the caller's own
	// goroutine — Recv and Close are already single-consumer by contract — so
	// it needs no lock of its own.
	//
	// 以下记录状态只会被调用方自己的协程碰到——按契约 Recv 与 Close 本就是单消费者
	// ——因此不需要为它单独加锁。
	metrics     *recorder
	clock       runtime.Clock
	operation   tunnelv1.Operation
	started     time.Time
	progressive bool
	sawFirst    bool
	outcome     error
	recordOnce  sync.Once
}

// call is the caller's half of a dispatched request, and the only thing the
// slot's read loop routes frames into.
type call struct {
	id     string
	frames chan *tunnelv1.AgentFrame

	// done is closed by Close and tells the read loop to stop trying to hand
	// over frames: the caller has gone.
	done     chan struct{}
	doneOnce sync.Once

	// aborted carries a failure raised outside the frame stream — the slot
	// died, the Agent broke framing — so a blocked Recv ends with the real
	// reason instead of a timeout.
	aborted   chan struct{}
	abortOnce sync.Once
	abortErr  error
}

func newCall(id string) *call {
	return &call{
		id:      id,
		frames:  make(chan *tunnelv1.AgentFrame),
		done:    make(chan struct{}),
		aborted: make(chan struct{}),
	}
}

// deliver hands one frame to the caller, giving up when the caller abandons
// the request or the slot's stream ends.
func (c *call) deliver(frame *tunnelv1.AgentFrame, slotDone <-chan struct{}) {
	select {
	case c.frames <- frame:
	case <-c.done:
	case <-slotDone:
	}
}

func (c *call) abort(err error) {
	c.abortOnce.Do(func() {
		c.abortErr = err
		close(c.aborted)
	})
}

func (c *call) release() {
	c.doneOnce.Do(func() { close(c.done) })
}

// Dispatch runs req on a parked slot of the named node and returns as soon as
// the request has been written, without waiting for the first response frame —
// waiting here would hide the node's time-to-first-token inside this call.
//
// With no idle slot it returns a backpressure *runtime.RuntimeError
// immediately. That is deliberate and is the whole reason slots are parked in
// advance: a scheduler learns "this node is full" in microseconds and places
// the request elsewhere, instead of queueing behind capacity this replica
// cannot see the end of.
func (s *Server) Dispatch(ctx context.Context, req Request) (*Response, error) {
	started := s.clock.Now()
	rec := s.metrics.forNode(req.NodeID)
	// failed records a dispatch that never became a Response. Everything that
	// returns before the Response is built goes through it, so the dispatch
	// counter's total is every attempt rather than only the ones that got far
	// enough to have a caller reading them.
	//
	// failed 记录一次从未变成 Response 的分发。所有在 Response 构造之前返回的路径都
	// 走它，因此分发计数器的总数是全部尝试，而不只是那些走到有调用方在读的部分。
	failed := func(err error) error {
		rec.Dispatch(req.Operation, tunnelwire.ResultFor(err), s.clock.Now().Sub(started))
		return err
	}

	if req.RuntimeID == "" {
		return nil, failed(dispatchError(runtime.ErrorInvalidConfig, req, "request carried no runtime_id", false, nil))
	}
	n, ok := s.lookup(req.NodeID)
	if !ok {
		return nil, failed(dispatchError(runtime.ErrorConnection, req, "node is not connected to this replica", true, ErrNodeNotConnected))
	}

	class := req.Class
	if class == tunnelv1.SlotClass_SLOT_CLASS_UNSPECIFIED {
		class = classFor(req.Operation)
	}
	sl := n.acquire(class)
	if sl == nil {
		return nil, failed(dispatchError(runtime.ErrorBackpressure, req, "node has no idle slot", true, ErrNoIdleSlot))
	}

	requestID := req.Trace["request_id"]
	if requestID == "" {
		requestID = newRequestID()
	}
	c := newCall(requestID)
	if err := sl.begin(c); err != nil {
		sl.close(err)
		return nil, failed(dispatchError(runtime.ErrorConnection, req, "slot closed before the request was written", true, err))
	}

	deadline := req.Deadline
	if ctxDeadline, ok := ctx.Deadline(); ok && (deadline.IsZero() || ctxDeadline.Before(deadline)) {
		deadline = ctxDeadline
	}
	var deadlineMs int64
	if !deadline.IsZero() {
		deadlineMs = deadline.UnixMilli()
	}

	headers := &tunnelv1.GatewayFrame{
		RequestId: requestID,
		Body: &tunnelv1.GatewayFrame_Headers{Headers: &tunnelv1.RequestHeaders{
			RuntimeId:      req.RuntimeID,
			Operation:      req.Operation,
			DeadlineUnixMs: deadlineMs,
			Payload:        req.Payload,
			Trace:          req.Trace,
		}},
	}
	if err := sl.send(headers); err != nil {
		sl.close(err)
		return nil, failed(dispatchError(runtime.ErrorConnection, req, "writing the request failed", true, err))
	}

	if req.Body != nil {
		send := func(chunk []byte) error {
			if len(chunk) > s.cfg.MaxFrameBytes {
				return dispatchError(runtime.ErrorProtocol, req, "request chunk exceeds the frame limit", false, nil)
			}
			return sl.send(&tunnelv1.GatewayFrame{
				RequestId: requestID,
				Body:      &tunnelv1.GatewayFrame_Data{Data: &tunnelv1.DataChunk{Payload: chunk}},
			})
		}
		if err := req.Body(send); err != nil {
			sl.close(err)
			return nil, failed(dispatchError(runtime.ErrorConnection, req, "writing the request body failed", true, err))
		}
	}

	// RequestEnd goes out for every operation, including those whose whole
	// request fit in the headers, so the Agent has one uniform signal that
	// no more input is coming.
	if err := sl.send(&tunnelv1.GatewayFrame{
		RequestId: requestID,
		Body:      &tunnelv1.GatewayFrame_End{End: &tunnelv1.RequestEnd{}},
	}); err != nil {
		sl.close(err)
		return nil, failed(dispatchError(runtime.ErrorConnection, req, "closing the request failed", true, err))
	}

	return &Response{
		ctx:       ctx,
		call:      c,
		slot:      sl,
		metrics:   rec,
		clock:     s.clock,
		operation: req.Operation,
		started:   started,
		// Only a progressive response has a meaningful time-to-first-frame:
		// for a request-response operation the first frame is the whole
		// answer, and mixing the two would leave neither distribution
		// readable. This is the same rule the Agent's own first-event metric
		// follows, which is what makes the two comparable.
		//
		// 只有渐进式响应才有有意义的首帧时间：一问一答的操作首帧就是全部答案，把
		// 两者混在一起会让两个分布都不可读。这与 Agent 自己的首帧指标遵循同一条
		// 规则，两者也正因此可以对照。
		progressive: progressiveResponse(req.Operation),
	}, nil
}

// progressiveResponse reports whether an operation's response arrives as more
// than one frame: a token stream, a workflow event stream or an artifact body.
// An operation this build does not know is treated as not progressive, so an
// unrecognized enum adds nothing to a latency distribution it may not belong
// in.
//
// progressiveResponse 报告某个操作的响应是否分多帧到达：token 流、工作流事件流，
// 或产物体。本次构建不认识的操作按非渐进式处理，这样一个无法识别的枚举就不会往一个
// 它未必属于的延迟分布里添数。
func progressiveResponse(op tunnelv1.Operation) bool {
	spec, err := tunnelwire.SpecFor(op)
	if err != nil {
		return false
	}
	return spec.Shape == tunnelwire.ShapeStream || spec.Shape == tunnelwire.ShapeBody
}

// Recv returns the next response payload. It returns io.EOF when the request
// finished successfully, and the error the Agent reported — restored to a
// *runtime.RuntimeError with its code, retryable flag and sentinel cause
// intact — when it did not.
//
// Each streaming operation's chunk is exactly one event, delivered as soon as
// the Agent produced it.
func (r *Response) Recv() ([]byte, error) {
	for {
		if r.finished {
			return nil, io.EOF
		}
		select {
		case frame := <-r.call.frames:
			switch {
			case frame.GetHeaders() != nil:
				// ResponseHeaders precede an artifact body; record and
				// keep reading so a caller only ever sees payloads.
				r.headers = frame.GetHeaders()
			case frame.GetEnd() != nil:
				r.finished = true
				if pb := frame.GetEnd().GetError(); pb != nil {
					err := tunnelwire.ErrorFromProto(pb)
					r.outcome = err
					return nil, err
				}
				return nil, io.EOF
			default:
				r.observeFirstEvent()
				return frame.GetData().GetPayload(), nil
			}
		case <-r.call.aborted:
			r.outcome = r.call.abortErr
			return nil, r.call.abortErr
		case <-r.ctx.Done():
			r.outcome = r.ctx.Err()
			return nil, r.ctx.Err()
		}
	}
}

// observeFirstEvent records the replica-side time to first response frame,
// once per response and only for a progressive one.
//
// observeFirstEvent 记录副本侧到首个响应帧的时间，每个响应只记一次，且只对渐进式
// 响应记录。
func (r *Response) observeFirstEvent() {
	if r.sawFirst || !r.progressive {
		return
	}
	r.sawFirst = true
	r.metrics.StreamFirstEvent(r.operation, r.clock.Now().Sub(r.started))
}

// Headers returns the ResponseHeaders of an artifact download. It is populated
// once the first Recv has returned, because ResponseHeaders always precede the
// body on the wire; it is nil for every other operation.
func (r *Response) Headers() *tunnelv1.ResponseHeaders { return r.headers }

// Close releases the request. When the response has not finished, it sends
// Cancel so the Agent stops work that nobody is waiting for any more; the slot
// stays open and the Agent re-parks it once it has drained.
func (r *Response) Close() error {
	if !r.finished {
		_ = r.slot.send(&tunnelv1.GatewayFrame{
			RequestId: r.call.id,
			Body:      &tunnelv1.GatewayFrame_Cancel{Cancel: &tunnelv1.Cancel{Reason: "gateway_cancel"}},
		})
		r.metrics.Cancel()
	}
	// Close, not the last Recv, is where a dispatch is counted: a caller may
	// stop reading at any point, and only Close is guaranteed to run for every
	// response. Once ensures a caller that closes twice does not double-count.
	//
	// 分发是在 Close 而不是最后一次 Recv 处计数的：调用方可能在任何位置停止读取，
	// 而只有 Close 才保证每个响应都会执行到。Once 保证重复调用 Close 的调用方不会
	// 把同一次分发算两遍。
	r.recordOnce.Do(func() {
		r.metrics.Dispatch(r.operation, tunnelwire.ResultFor(r.outcome), r.clock.Now().Sub(r.started))
	})
	r.call.release()
	return nil
}

// classFor picks the slot class an operation belongs on. Artifact transfer is
// the one bulk operation: a multi-hundred-megabyte download would otherwise
// occupy an inference slot for minutes and wreck the node's TTFT.
func classFor(op tunnelv1.Operation) tunnelv1.SlotClass {
	if op == tunnelv1.Operation_OPERATION_ARTIFACT_OPEN {
		return tunnelv1.SlotClass_SLOT_CLASS_BULK
	}
	return tunnelv1.SlotClass_SLOT_CLASS_INFERENCE
}

// dispatchError builds the *runtime.RuntimeError a dispatch failure is
// reported as, so a Gateway caller sees the same error model whether the
// failure happened on the node or before the request ever left this replica.
func dispatchError(code runtime.ErrorCode, req Request, msg string, retryable bool, cause error) error {
	return &runtime.RuntimeError{
		Code:      code,
		RuntimeID: req.RuntimeID,
		Operation: tunnelwire.OperationName(req.Operation),
		Message:   msg,
		Retryable: retryable,
		Cause:     cause,
	}
}

// errSlotEndedEarly is the cause reported when a slot's stream ends before the
// response did.
var errSlotEndedEarly = errors.New("tunnelserver: slot ended before the response did")

// slotDeathError is what an in-flight request is aborted with when its slot
// dies. It is deliberately not retryable: this replica cannot know whether the
// Agent had already produced tokens that reached the caller, and the tunnel
// design forbids transparently retrying a request that may have. A scheduler
// that wants to retry an uncommitted request must decide that from its own
// view of what it has already written downstream.
func slotDeathError(cause error) error {
	if cause == nil || errors.Is(cause, io.EOF) {
		cause = errSlotEndedEarly
	}
	return &runtime.RuntimeError{
		Code:      runtime.ErrorConnection,
		Operation: "tunnel_dispatch",
		Message:   "the tunnel slot closed before the response finished",
		Retryable: false,
		Cause:     cause,
	}
}

// newRequestID mints the request_id carried on every frame of one request. It
// is random rather than sequential because it appears in both sides' logs and
// must not leak how many requests a replica has served.
func newRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failing is not a condition a request can recover
		// from, but a correlation id is not worth killing a request over:
		// fall back to a value that is still unique per process.
		return "req-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf[:])
}
