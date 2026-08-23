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
	if req.RuntimeID == "" {
		return nil, dispatchError(runtime.ErrorInvalidConfig, req, "request carried no runtime_id", false, nil)
	}
	n, ok := s.lookup(req.NodeID)
	if !ok {
		return nil, dispatchError(runtime.ErrorConnection, req, "node is not connected to this replica", true, ErrNodeNotConnected)
	}

	class := req.Class
	if class == tunnelv1.SlotClass_SLOT_CLASS_UNSPECIFIED {
		class = classFor(req.Operation)
	}
	sl := n.acquire(class)
	if sl == nil {
		return nil, dispatchError(runtime.ErrorBackpressure, req, "node has no idle slot", true, ErrNoIdleSlot)
	}

	requestID := req.Trace["request_id"]
	if requestID == "" {
		requestID = newRequestID()
	}
	c := newCall(requestID)
	if err := sl.begin(c); err != nil {
		sl.close(err)
		return nil, dispatchError(runtime.ErrorConnection, req, "slot closed before the request was written", true, err)
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
		return nil, dispatchError(runtime.ErrorConnection, req, "writing the request failed", true, err)
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
			return nil, dispatchError(runtime.ErrorConnection, req, "writing the request body failed", true, err)
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
		return nil, dispatchError(runtime.ErrorConnection, req, "closing the request failed", true, err)
	}

	return &Response{ctx: ctx, call: c, slot: sl}, nil
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
					return nil, tunnelwire.ErrorFromProto(pb)
				}
				return nil, io.EOF
			default:
				return frame.GetData().GetPayload(), nil
			}
		case <-r.call.aborted:
			return nil, r.call.abortErr
		case <-r.ctx.Done():
			return nil, r.ctx.Err()
		}
	}
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
	}
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
