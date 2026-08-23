package tunnel

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
)

// This file is one data-plane slot: a single Serve stream that the Agent
// opens, parks with Ready, and then reuses request after request. A slot
// serves exactly one request at a time, which is what lets it inherit
// HTTP/2's per-stream flow control instead of growing a hand-rolled
// multiplexer — and what makes a stuck 500MB artifact download somebody
// else's problem rather than a queue in front of every waiting token.
//
// The slot owns the frame loop and nothing else. It does not know what a
// runtime is, how an Operation is encoded or which runtime_id is allowed:
// all of that lives behind Handler, which dispatch.go (阶段 5) implements.
//
// Two goroutines touch one slot: the receive loop (slot.run) and, while a
// request is running, the handler goroutine. Every frame the Agent sends
// passes through slot.send, which serializes writes — a gRPC stream tolerates
// exactly one concurrent sender.

// slotOperation is the Operation recorded on errors the slot itself raises,
// so a frame-loop failure is attributable without a runtime.
const slotOperation = "tunnel_slot"

// Handler executes one request that arrived on a slot. dispatch.go (阶段 5)
// implements it against runtime.Manager; a returned error terminates the
// request with that error in ResponseEnd, and nil terminates it successfully.
//
// A Handler must return promptly once ctx is done: the slot cannot be reused,
// nor the tunnel drained, until it does.
type Handler interface {
	Handle(ctx context.Context, req *Request, sink ResponseSink) error
}

// HandlerFunc adapts an ordinary function to Handler.
type HandlerFunc func(ctx context.Context, req *Request, sink ResponseSink) error

// Handle implements Handler.
func (f HandlerFunc) Handle(ctx context.Context, req *Request, sink ResponseSink) error {
	return f(ctx, req, sink)
}

// Request is one dispatched request as the slot sees it: the headers exactly
// as the replica sent them, plus the body chunks that follow.
type Request struct {
	// ID is the request_id carried on every frame of this request.
	ID string
	// SlotID and Class identify the slot serving it, for logs and metrics.
	SlotID string
	Class  tunnelv1.SlotClass
	// Headers is the dispatched RequestHeaders: runtime_id, operation,
	// deadline and payload. The Handler owns validating all of it.
	Headers *tunnelv1.RequestHeaders
	// Body yields the request body chunks in order and is closed on
	// RequestEnd. Most operations carry no body and never send on it. A
	// Handler that ignores Body costs nothing: once it returns, the slot
	// drops any remaining chunks rather than buffering them.
	Body <-chan []byte
}

// ResponseSink is how a Handler writes its answer. Each call sends exactly
// one frame, immediately: a streaming operation must call Data once per event
// as the event is produced, because aggregating them is precisely what turns
// a millisecond time-to-first-token into a second.
type ResponseSink interface {
	// Headers sends ResponseHeaders. Only ARTIFACT_OPEN uses it; size is -1
	// when the length is unknown.
	Headers(contentType string, size int64) error
	// Data sends one DataChunk.
	Data(payload []byte) error
}

// errSlotRetired and errRequestFinished are the causes a slot attaches when
// it cancels work of its own accord, so a Handler blocked on ctx.Done can
// tell "the tunnel is going away" from "you already returned".
var (
	errSlotRetired     = errors.New("tunnel: slot retired")
	errRequestFinished = errors.New("tunnel: request finished")
)

// slot is one Serve stream and its reuse loop.
type slot struct {
	pool   *Pool
	id     string
	class  tunnelv1.SlotClass
	logger *slog.Logger

	// created is when the slot was opened, for the age-based rotation.
	created time.Time

	// cancel tears this slot down: it cancels the stream context, which
	// unblocks Recv and every in-flight handler. Retiring one slot never
	// touches its siblings — they are separate streams with separate
	// contexts, which is what keeps a single protocol fault from cascading.
	cancel context.CancelFunc

	// sendMu serializes writes to stream, the only field guarded by it.
	sendMu sync.Mutex
	stream ServeStream

	// mu guards the request state below.
	mu     sync.Mutex
	active *slotRequest
	served int

	// poolParked, poolIdle, poolIdleSince and poolReaping are the pool's view
	// of this slot. They are guarded by Pool.mu, not by mu, and must only be
	// touched through the pool's slotIdle/slotBusy/slotFinished callbacks and
	// its reconcile.
	poolParked    bool
	poolIdle      bool
	poolIdleSince time.Time
	// poolReaping marks a slot the reconcile has already decided to close.
	// Closing is asynchronous — it takes the slot's own goroutine unwinding
	// to remove it from the pool — and a second reconcile in that window
	// would otherwise see it as an idle slot still available for reaping and
	// close a second one, taking the pool below its floor.
	poolReaping bool
}

// slotRequest is the request currently on a slot.
type slotRequest struct {
	id     string
	ctx    context.Context
	cancel context.CancelCauseFunc
	body   chan []byte

	// started is when RequestHeaders arrived, which is where both the
	// duration and the tunnel-side TTFT are measured from: everything after
	// it is time this Agent is accountable for.
	started time.Time
	// op and streaming describe the request for the metric labels. streaming
	// is false for an operation this build does not know, so an unknown
	// operation cannot land in the TTFT histogram.
	op        tunnelv1.Operation
	streaming bool

	// bodyClosed guards against a replica sending RequestEnd twice, which
	// would otherwise close an already-closed channel.
	bodyClosed bool
}

// run opens the stream, parks the slot and serves requests until the stream
// breaks, ctx ends or the slot rotates. It always ends by telling the pool,
// which decides whether to open a replacement.
func (s *slot) run(ctx context.Context) {
	defer s.pool.slotFinished(s)

	streamCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	defer cancel()

	stream, err := s.pool.transport.Serve(streamCtx)
	if err != nil {
		s.pool.metrics.SlotAcquireFailure(s.class)
		s.pool.slotOpenFailed()
		s.logger.Warn("cannot open a serve slot", slog.String("error", err.Error()))
		return
	}
	s.stream = stream

	// Teardown order matters and reads bottom-up: cancel the stream context
	// so handlers unblock, wait for them so no goroutine outlives the slot,
	// then half-close. Anything else either leaks a handler or closes the
	// stream out from under one.
	var handlers sync.WaitGroup
	defer func() { _ = stream.CloseSend() }()
	defer handlers.Wait()
	defer cancel()

	if err := s.park(); err != nil {
		s.pool.metrics.SlotAcquireFailure(s.class)
		s.pool.slotOpenFailed()
		s.logger.Warn("cannot park a serve slot", slog.String("error", err.Error()))
		return
	}

	for {
		frame, err := stream.Recv()
		if err != nil {
			s.abort(err)
			if streamCtx.Err() == nil {
				s.logger.Debug("serve slot closed", slog.String("error", err.Error()))
			}
			return
		}
		s.pool.metrics.FrameBytes(DirectionInbound, proto.Size(frame))
		if !s.handleFrame(streamCtx, &handlers, frame) {
			return
		}
	}
}

// handleFrame dispatches one replica frame, reporting whether the slot lives
// on. It never blocks on the Handler: a request runs in its own goroutine so
// body chunks and Cancel keep flowing while it does.
func (s *slot) handleFrame(ctx context.Context, handlers *sync.WaitGroup, frame *tunnelv1.GatewayFrame) bool {
	switch body := frame.GetBody().(type) {
	case *tunnelv1.GatewayFrame_Ping:
		return s.send(&tunnelv1.AgentFrame{Body: &tunnelv1.AgentFrame_Pong{
			Pong: &tunnelv1.Pong{SentUnixMs: body.Ping.GetSentUnixMs()},
		}}) == nil

	case *tunnelv1.GatewayFrame_Headers:
		return s.begin(ctx, handlers, frame.GetRequestId(), body.Headers)

	case *tunnelv1.GatewayFrame_Data:
		return s.feed(frame.GetRequestId(), body.Data.GetPayload())

	case *tunnelv1.GatewayFrame_End:
		return s.closeBody(frame.GetRequestId())

	case *tunnelv1.GatewayFrame_Cancel:
		s.cancelRequest(frame.GetRequestId(), body.Cancel.GetReason())
		return true

	default:
		// Unknown frames are ignored on purpose, so a newer Gateway can add
		// data-plane frames without breaking older Agents.
		s.logger.Debug("ignoring unknown serve frame", slog.String("request_id", frame.GetRequestId()))
		return true
	}
}

// begin starts one request on this slot.
func (s *slot) begin(ctx context.Context, handlers *sync.WaitGroup, id string, headers *tunnelv1.RequestHeaders) bool {
	if id == "" {
		return s.fault("", "RequestHeaders carried no request_id")
	}

	s.mu.Lock()
	if s.active != nil {
		s.mu.Unlock()
		return s.fault(id, "a request was dispatched while this slot was still busy")
	}
	reqCtx, cancel := context.WithCancelCause(ctx)
	spec, specErr := tunnelwire.SpecFor(headers.GetOperation())
	req := &slotRequest{
		id:        id,
		ctx:       reqCtx,
		cancel:    cancel,
		body:      make(chan []byte),
		started:   s.pool.clock.Now(),
		op:        headers.GetOperation(),
		streaming: specErr == nil && (spec.Shape == tunnelwire.ShapeStream || spec.Shape == tunnelwire.ShapeBody),
	}
	s.active = req
	s.mu.Unlock()

	s.pool.slotBusy(s)
	handlers.Add(1)
	go func() {
		defer handlers.Done()
		s.serve(req, headers)
	}()
	return true
}

// serve runs the Handler, terminates the request with ResponseEnd, and then
// either parks the slot again or retires it for rotation.
func (s *slot) serve(req *slotRequest, headers *tunnelv1.RequestHeaders) {
	err := s.invoke(req, headers)
	duration := s.pool.clock.Now().Sub(req.started)
	s.pool.metrics.Request(req.op, resultFor(err), duration)

	// Cancelling before anything else releases a receive loop that may be
	// blocked handing the handler a body chunk it will never read.
	req.cancel(errRequestFinished)

	s.mu.Lock()
	s.active = nil
	s.served++
	served := s.served
	s.mu.Unlock()

	log := s.logger.With(
		slog.String("request_id", req.id),
		slog.String("runtime_id", headers.GetRuntimeId()),
		slog.String("operation", headers.GetOperation().String()),
		slog.Duration("duration", duration))
	if err != nil {
		// Only the code and the sanitized message reach the log; the payload
		// never does.
		log.Info("request failed", slog.String("code", errorCode(err)))
	} else {
		log.Debug("request served")
	}

	if sendErr := s.sendEnd(req.id, err); sendErr != nil {
		s.retire("send response end failed")
		return
	}
	if reason, expired := s.expired(served); expired {
		s.retire(reason)
		return
	}
	if err := s.park(); err != nil {
		s.retire("park failed")
	}
}

// invoke calls the Handler with the panic barrier around it. A panicking
// handler must cost one request and one slot, never the tunnel: the stack
// goes to the local log and the caller gets a fixed message, because a panic
// value can quote request content that must not cross the wire.
func (s *slot) invoke(req *slotRequest, headers *tunnelv1.RequestHeaders) (err error) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("request handler panicked",
				slog.String("request_id", req.id),
				slog.String("stack", string(debug.Stack())))
			err = &runtime.RuntimeError{
				Code:      runtime.ErrorUpstream,
				Operation: slotOperation,
				Message:   "the agent failed to serve this request",
			}
		}
	}()

	return s.pool.handler.Handle(req.ctx, &Request{
		ID:      req.id,
		SlotID:  s.id,
		Class:   s.class,
		Headers: headers,
		Body:    req.body,
	}, &slotSink{slot: s, req: req})
}

// feed hands one body chunk to the running request. A chunk that names some
// other request is dropped for the same reason a late Cancel is: a handler
// that answered early leaves body frames in flight behind it, and killing the
// slot over them would make an ordinary early return cost a stream.
//
// The select is what keeps the receive loop from blocking forever on a
// handler that never reads its body — and what keeps the slot from buffering
// a body nobody wants.
func (s *slot) feed(id string, payload []byte) bool {
	req := s.match(id)
	if req == nil {
		s.logger.Debug("dropping a body chunk for a request that is not running on this slot",
			slog.String("request_id", id))
		return true
	}
	select {
	case req.body <- payload:
	case <-req.ctx.Done():
	}
	return true
}

// closeBody signals end of request body.
func (s *slot) closeBody(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.active.id != id {
		return true
	}
	if !s.active.bodyClosed {
		s.active.bodyClosed = true
		close(s.active.body)
	}
	return true
}

// cancelRequest cancels the running request, but only when the Cancel names
// it. Dropping the mismatches is the whole defence against slot reuse: a
// cancel for a request that already ended would otherwise kill whichever
// request took the slot next.
func (s *slot) cancelRequest(id, reason string) {
	req := s.match(id)
	if req == nil {
		s.logger.Debug("dropping a cancel for a request that is not running on this slot",
			slog.String("request_id", id))
		return
	}
	// The replica's reason is free-form text and stays in the debug log; the
	// metric gets the bounded classification instead.
	s.logger.Debug("cancelling request", slog.String("request_id", id), slog.String("reason", reason))
	s.pool.metrics.Cancel(CancelByGateway)
	req.cancel(context.Canceled)
}

// match returns the running request when it is the one named by id.
func (s *slot) match(id string) *slotRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || id == "" || s.active.id != id {
		return nil
	}
	return s.active
}

// park sends Ready and hands the slot back to the pool's idle set.
func (s *slot) park() error {
	if err := s.send(&tunnelv1.AgentFrame{Body: &tunnelv1.AgentFrame_Ready{Ready: &tunnelv1.Ready{
		NodeId: s.pool.cfg.NodeID,
		SlotId: s.id,
		Class:  s.class,
	}}}); err != nil {
		return err
	}
	s.pool.slotIdle(s)
	return nil
}

// sendEnd terminates a request. A nil err is a successful ResponseEnd.
func (s *slot) sendEnd(id string, err error) error {
	return s.send(&tunnelv1.AgentFrame{
		RequestId: id,
		Body:      &tunnelv1.AgentFrame_End{End: &tunnelv1.ResponseEnd{Error: tunnelwire.ErrorToProto(err)}},
	})
}

// send writes one frame under the send lock.
func (s *slot) send(frame *tunnelv1.AgentFrame) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if err := s.stream.Send(frame); err != nil {
		return err
	}
	// Measured after the send, so the histogram describes bytes that left
	// rather than bytes that were offered to a broken stream.
	s.pool.metrics.FrameBytes(DirectionOutbound, proto.Size(frame))
	return nil
}

// fault ends the slot on a protocol error, telling the replica about the
// request it named first. It closes this slot and no other: the sibling slots
// are separate streams and keep serving.
func (s *slot) fault(id, detail string) bool {
	s.logger.Error("serve slot protocol error; closing this slot only",
		slog.String("request_id", id),
		slog.String("detail", detail))
	if id != "" {
		_ = s.sendEnd(id, &runtime.RuntimeError{
			Code:      runtime.ErrorProtocol,
			Operation: slotOperation,
			Message:   detail,
		})
	}
	s.abort(errors.New(detail))
	return false
}

// retire closes the slot deliberately: rotation, idle reaping or a failed
// send. The pool opens a replacement if the watermarks still call for one.
func (s *slot) retire(reason string) {
	s.logger.Debug("retiring serve slot", slog.String("reason", reason))
	s.abort(errSlotRetired)
}

// abort cancels the slot's stream context, which ends the receive loop and
// every handler running on it.
func (s *slot) abort(cause error) {
	s.mu.Lock()
	req := s.active
	s.mu.Unlock()
	if req != nil {
		s.pool.metrics.Cancel(CancelSlotClosed)
		req.cancel(cause)
	}
	if s.cancel != nil {
		s.cancel()
	}
}

// expired reports whether the slot has reached a rotation limit. Rebuilding a
// slot after a bounded number of requests or a bounded lifetime is cheap
// insurance against state quietly accumulating on a connection that would
// otherwise live for weeks.
func (s *slot) expired(served int) (string, bool) {
	limits := s.pool.cfg.Slots
	if limits.MaxRequestsPerSlot > 0 && served >= limits.MaxRequestsPerSlot {
		return "request limit reached", true
	}
	if limits.MaxSlotAge > 0 && !s.pool.clock.Now().Before(s.created.Add(limits.MaxSlotAge)) {
		return "age limit reached", true
	}
	return "", false
}

// errorCode renders an error's code for a log line, without its message: the
// code is the part that is safe by construction, and the sanitized message
// already travels to the replica inside ResponseEnd.
func errorCode(err error) string {
	var re *runtime.RuntimeError
	if errors.As(err, &re) {
		return string(re.Code)
	}
	code, _ := tunnelwire.ClassifyBareError(err)
	return string(code)
}

// slotSink is the ResponseSink handed to one request's Handler. It is scoped
// to that request, so a handler that outlives its request cannot write frames
// onto whatever request reused the slot.
type slotSink struct {
	slot *slot
	req  *slotRequest
	// first fires once, on the first response frame that leaves this sink.
	first sync.Once
}

// Headers implements ResponseSink.
func (w *slotSink) Headers(contentType string, size int64) error {
	if err := w.closed(); err != nil {
		return err
	}
	w.observeFirst()
	return w.slot.send(&tunnelv1.AgentFrame{
		RequestId: w.req.id,
		Body: &tunnelv1.AgentFrame_Headers{Headers: &tunnelv1.ResponseHeaders{
			ContentType: contentType,
			Size:        size,
		}},
	})
}

// Data implements ResponseSink.
func (w *slotSink) Data(payload []byte) error {
	if err := w.closed(); err != nil {
		return err
	}
	w.observeFirst()
	return w.slot.send(&tunnelv1.AgentFrame{
		RequestId: w.req.id,
		Body:      &tunnelv1.AgentFrame_Data{Data: &tunnelv1.DataChunk{Payload: payload}},
	})
}

// observeFirst records the tunnel-side TTFT: the interval between
// RequestHeaders arriving and the first response frame going out. Only
// progressive responses — event streams and artifact bodies — are measured.
// For a single-response operation the first frame is the whole answer, so
// mixing the two would leave a histogram that means neither.
func (w *slotSink) observeFirst() {
	if !w.req.streaming {
		return
	}
	w.first.Do(func() {
		w.slot.pool.metrics.StreamFirstEvent(w.req.op, w.slot.pool.clock.Now().Sub(w.req.started))
	})
}

// closed reports the request's own cancellation, so a handler that keeps
// writing after a cancel gets an error instead of racing the next request.
func (w *slotSink) closed() error {
	if err := w.req.ctx.Err(); err != nil {
		return &runtime.RuntimeError{
			Code:      runtime.ErrorConnection,
			Operation: slotOperation,
			Message:   "the request is no longer running on this slot",
			Cause:     context.Cause(w.req.ctx),
		}
	}
	return nil
}
