package tunnel_test

import (
	"context"
	"errors"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
)

// These tests drive one slot at a time through its frame loop. soloSlot pins
// the pool to a single inference slot so a test observes exactly the stream it
// is talking to, with no replacement racing into the assertions.
func soloSlot(cfg *tunnel.PoolConfig) {
	cfg.Slots.MinSlots = 1
	cfg.Slots.LowWatermark = 1
	cfg.Slots.NodeTotalSlots = 1
	cfg.Slots.BulkSlots = -1
}

func TestSlotServesRequestsInSequenceOnOneStream(t *testing.T) {
	f := newPoolFixture(t, soloSlot)
	f.handler.set(func(_ context.Context, req *tunnel.Request, sink tunnel.ResponseSink) error {
		return sink.Data([]byte(req.ID))
	})
	f.start()

	c := f.acceptSlots(1)[0]
	for _, id := range []string{"req-1", "req-2", "req-3"} {
		f.dispatch(c, id)
		end, chunks := f.expectEnd(c, id)
		if end.GetError() != nil {
			t.Fatalf("request %s failed: %v", id, end.GetError())
		}
		if len(chunks) != 1 || string(chunks[0]) != id {
			t.Fatalf("chunks for %s = %q, want one chunk %q", id, chunks, id)
		}
		f.expectReady(c)
	}

	// One stream served all three: reuse is what removes stream setup from
	// the request path.
	if got := f.gw.ServeDials(); got != 1 {
		t.Errorf("serve streams opened = %d, want %d", got, 1)
	}
}

func TestSlotDropsALateCancelForAReusedRequest(t *testing.T) {
	f := newPoolFixture(t, soloSlot)

	release := make(chan struct{})
	f.handler.set(func(ctx context.Context, req *tunnel.Request, _ tunnel.ResponseSink) error {
		if req.ID != "req-2" {
			return nil
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	f.start()

	c := f.acceptSlots(1)[0]

	// The first request finishes and the slot parks itself again.
	f.dispatch(c, "req-1")
	if end, _ := f.expectEnd(c, "req-1"); end.GetError() != nil {
		t.Fatalf("req-1 failed: %v", end.GetError())
	}
	f.expectReady(c)
	f.handler.waitCall(t)

	// A cancel for the finished request arrives after it ended, then a new
	// request reuses the slot. Frames are ordered, so the late cancel is
	// handled first: it must be dropped, not applied to whoever took the slot
	// next.
	f.sendFrame(c, &tunnelv1.GatewayFrame{
		RequestId: "req-1",
		Body:      &tunnelv1.GatewayFrame_Cancel{Cancel: &tunnelv1.Cancel{Reason: "client went away"}},
	})
	f.dispatch(c, "req-2")

	call := f.handler.waitCall(t)
	if call.req.ID != "req-2" {
		t.Fatalf("dispatched request = %q, want %q", call.req.ID, "req-2")
	}
	if err := call.ctx.Err(); err != nil {
		t.Fatalf("the reused slot's request was already cancelled: %v", err)
	}

	close(release)
	end, _ := f.expectEnd(c, "req-2")
	if end.GetError() != nil {
		t.Errorf("req-2 ended with %v, want success: a late cancel killed a reused slot", end.GetError())
	}
	f.expectReady(c)
}

func TestSlotCancelStopsTheMatchingRequest(t *testing.T) {
	f := newPoolFixture(t, soloSlot)
	f.handler.set(func(ctx context.Context, _ *tunnel.Request, _ tunnel.ResponseSink) error {
		<-ctx.Done()
		return context.Cause(ctx)
	})
	f.start()

	c := f.acceptSlots(1)[0]
	f.dispatch(c, "req-1")
	f.handler.waitCall(t)

	f.sendFrame(c, &tunnelv1.GatewayFrame{
		RequestId: "req-1",
		Body:      &tunnelv1.GatewayFrame_Cancel{Cancel: &tunnelv1.Cancel{Reason: "client went away"}},
	})

	end, _ := f.expectEnd(c, "req-1")
	if got := end.GetError().GetCode(); got != string(runtime.ErrorConnection) {
		t.Errorf("cancelled request ended with code %q, want %q", got, runtime.ErrorConnection)
	}
	// A cancelled request still returns the slot: cancelling one request must
	// not cost the stream it ran on.
	f.expectReady(c)
}

func TestSlotProtocolErrorClosesOnlyThatSlot(t *testing.T) {
	f := newPoolFixture(t, func(cfg *tunnel.PoolConfig) {
		cfg.Slots.BulkSlots = -1
	})
	f.handler.set(func(ctx context.Context, req *tunnel.Request, _ tunnel.ResponseSink) error {
		if req.ID != "req-1" {
			return nil
		}
		<-ctx.Done()
		return ctx.Err()
	})
	f.start()

	conns := f.acceptSlots(2)
	broken, healthy := conns[0], conns[1]

	// A slot serves one request at a time. A second dispatch while the first
	// is still running means the two sides disagree about the slot's state,
	// and no further frame on it can be trusted.
	f.dispatch(broken, "req-1")
	f.handler.waitCall(t)
	f.dispatch(broken, "req-2")

	end, _ := f.expectEnd(broken, "req-2")
	if got := end.GetError().GetCode(); got != string(runtime.ErrorProtocol) {
		t.Errorf("protocol fault ended with code %q, want %q", got, runtime.ErrorProtocol)
	}
	f.expectClosed(broken)

	// The sibling slot is a separate stream and keeps serving; the pool opens
	// a replacement for the one that died.
	f.dispatch(healthy, "req-3")
	if end, _ := f.expectEnd(healthy, "req-3"); end.GetError() != nil {
		t.Errorf("a healthy slot failed after a sibling's protocol error: %v", end.GetError())
	}
	f.expectReady(healthy)

	f.acceptSlots(1)
	f.waitStats("two idle slots again", func(s tunnel.PoolStats) bool { return s.Inference.Idle == 2 })
}

func TestSlotClosesOnHeadersWithNoRequestID(t *testing.T) {
	f := newPoolFixture(t, soloSlot)
	f.start()

	c := f.acceptSlots(1)[0]
	// Without a request_id nothing on the slot can be correlated — not the
	// response, not a cancel. There is nobody to send a ResponseEnd to, so
	// the slot simply goes.
	f.sendFrame(c, &tunnelv1.GatewayFrame{
		Body: &tunnelv1.GatewayFrame_Headers{Headers: &tunnelv1.RequestHeaders{
			RuntimeId: "ollama-local",
			Operation: tunnelv1.Operation_OPERATION_CHAT,
		}},
	})

	f.expectClosed(c)
	f.acceptSlots(1)
	f.waitStats("a replacement slot", func(s tunnel.PoolStats) bool { return s.Inference.Idle == 1 })
}

func TestSlotRotatesAfterTheRequestLimit(t *testing.T) {
	f := newPoolFixture(t, func(cfg *tunnel.PoolConfig) {
		soloSlot(cfg)
		cfg.Slots.MaxRequestsPerSlot = 2
	})
	f.start()

	c := f.acceptSlots(1)[0]

	f.dispatch(c, "req-1")
	f.expectEnd(c, "req-1")
	f.expectReady(c)

	// The second request reaches the limit: the slot terminates the request
	// normally and then retires instead of parking again.
	f.dispatch(c, "req-2")
	if end, _ := f.expectEnd(c, "req-2"); end.GetError() != nil {
		t.Fatalf("the last request before rotation failed: %v", end.GetError())
	}
	f.expectClosed(c)

	// The watermark is unchanged, so a fresh slot takes its place.
	replacement := f.acceptSlots(1)[0]
	if replacement.ready.GetSlotId() == c.ready.GetSlotId() {
		t.Errorf("the replacement reused slot id %q", c.ready.GetSlotId())
	}
	f.waitStats("one idle slot again", func(s tunnel.PoolStats) bool { return s.Inference.Idle == 1 })
}

func TestSlotRotatesAfterTheAgeLimit(t *testing.T) {
	f := newPoolFixture(t, func(cfg *tunnel.PoolConfig) {
		soloSlot(cfg)
		cfg.Slots.MaxSlotAge = time.Hour
	})
	f.start()

	c := f.acceptSlots(1)[0]
	f.clock.Advance(time.Hour + time.Minute)

	// An idle slot is rebuilt on age alone: waiting for traffic would let a
	// stream on a quiet node live indefinitely.
	f.expectClosed(c)
	replacement := f.acceptSlots(1)[0]
	if replacement.ready.GetSlotId() == c.ready.GetSlotId() {
		t.Errorf("the replacement reused slot id %q", c.ready.GetSlotId())
	}
	f.waitStats("one idle slot again", func(s tunnel.PoolStats) bool { return s.Inference.Idle == 1 })
}

func TestSlotStreamsTheRequestBodyAndTheResponse(t *testing.T) {
	f := newPoolFixture(t, soloSlot)
	f.handler.set(func(_ context.Context, req *tunnel.Request, sink tunnel.ResponseSink) error {
		if err := sink.Headers("application/octet-stream", -1); err != nil {
			return err
		}
		for chunk := range req.Body {
			if err := sink.Data(chunk); err != nil {
				return err
			}
		}
		return nil
	})
	f.start()

	c := f.acceptSlots(1)[0]
	f.dispatch(c, "req-1")
	for _, part := range []string{"one", "two", "three"} {
		f.sendFrame(c, &tunnelv1.GatewayFrame{
			RequestId: "req-1",
			Body:      &tunnelv1.GatewayFrame_Data{Data: &tunnelv1.DataChunk{Payload: []byte(part)}},
		})
	}
	f.sendFrame(c, &tunnelv1.GatewayFrame{
		RequestId: "req-1",
		Body:      &tunnelv1.GatewayFrame_End{End: &tunnelv1.RequestEnd{}},
	})

	end, chunks := f.expectEnd(c, "req-1")
	if end.GetError() != nil {
		t.Fatalf("the streamed request failed: %v", end.GetError())
	}
	want := []string{"one", "two", "three"}
	if len(chunks) != len(want) {
		t.Fatalf("response chunks = %d, want %d: events must be forwarded one by one", len(chunks), len(want))
	}
	for i, w := range want {
		if string(chunks[i]) != w {
			t.Errorf("chunk %d = %q, want %q", i, chunks[i], w)
		}
	}
	f.expectReady(c)
}

func TestSlotIgnoresABodyNobodyReads(t *testing.T) {
	f := newPoolFixture(t, soloSlot)
	f.handler.set(func(context.Context, *tunnel.Request, tunnel.ResponseSink) error { return nil })
	f.start()

	c := f.acceptSlots(1)[0]
	f.dispatch(c, "req-1")

	// The handler returns without touching Body. The chunks that follow must
	// be dropped rather than buffered or blocked on — the slot has to stay
	// reusable either way.
	for range 4 {
		f.sendFrame(c, &tunnelv1.GatewayFrame{
			RequestId: "req-1",
			Body:      &tunnelv1.GatewayFrame_Data{Data: &tunnelv1.DataChunk{Payload: []byte("ignored")}},
		})
	}

	if end, _ := f.expectEnd(c, "req-1"); end.GetError() != nil {
		t.Fatalf("req-1 failed: %v", end.GetError())
	}
	f.expectReady(c)

	f.dispatch(c, "req-2")
	if end, _ := f.expectEnd(c, "req-2"); end.GetError() != nil {
		t.Errorf("the slot was unusable after an unread body: %v", end.GetError())
	}
}

func TestSlotAnswersPing(t *testing.T) {
	f := newPoolFixture(t, soloSlot)
	f.start()

	c := f.acceptSlots(1)[0]
	f.sendFrame(c, &tunnelv1.GatewayFrame{
		Body: &tunnelv1.GatewayFrame_Ping{Ping: &tunnelv1.Ping{SentUnixMs: 4242}},
	})

	frame := f.recvFrame(c)
	pong := frame.GetPong()
	if pong == nil {
		t.Fatalf("answer to Ping was %T, want Pong", frame.GetBody())
	}
	if pong.GetSentUnixMs() != 4242 {
		t.Errorf("Pong.sent_unix_ms = %d, want %d: the timestamp must be echoed", pong.GetSentUnixMs(), 4242)
	}
}

func TestSlotReportsAHandlerError(t *testing.T) {
	f := newPoolFixture(t, soloSlot)
	f.handler.set(func(context.Context, *tunnel.Request, tunnel.ResponseSink) error {
		return &runtime.RuntimeError{
			Code:      runtime.ErrorBackpressure,
			RuntimeID: "ollama-local",
			Message:   "no capacity",
			Retryable: true,
		}
	})
	f.start()

	c := f.acceptSlots(1)[0]
	f.dispatch(c, "req-1")

	end, _ := f.expectEnd(c, "req-1")
	if got := end.GetError().GetCode(); got != string(runtime.ErrorBackpressure) {
		t.Errorf("ResponseEnd code = %q, want %q", got, runtime.ErrorBackpressure)
	}
	if !end.GetError().GetRetryable() {
		t.Error("ResponseEnd dropped the retryable flag; the replica cannot pick another node without it")
	}
	// A failed request costs no slot: the failure was the runtime's, not the
	// stream's.
	f.expectReady(c)
}

func TestSlotSurvivesAPanickingHandler(t *testing.T) {
	f := newPoolFixture(t, soloSlot)
	f.handler.set(func(context.Context, *tunnel.Request, tunnel.ResponseSink) error {
		panic("handler exploded with sk-secret-key in scope")
	})
	f.start()

	c := f.acceptSlots(1)[0]
	f.dispatch(c, "req-1")

	end, _ := f.expectEnd(c, "req-1")
	if end.GetError() == nil {
		t.Fatal("a panicking handler reported success")
	}
	if got := end.GetError().GetMessage(); got == "" || contains(got, "sk-secret-key") {
		t.Errorf("ResponseEnd message = %q; a panic value must not reach the wire", got)
	}
	f.expectReady(c)
}

func TestSlotWritesAfterCancellationAreRefused(t *testing.T) {
	f := newPoolFixture(t, soloSlot)

	writeErr := make(chan error, 1)
	started := make(chan struct{})
	f.handler.set(func(ctx context.Context, _ *tunnel.Request, sink tunnel.ResponseSink) error {
		close(started)
		<-ctx.Done()
		// A handler that keeps writing after its request was cancelled must
		// get an error rather than push frames onto the next request.
		writeErr <- sink.Data([]byte("too late"))
		return context.Cause(ctx)
	})
	f.start()

	c := f.acceptSlots(1)[0]
	f.dispatch(c, "req-1")
	<-started

	f.sendFrame(c, &tunnelv1.GatewayFrame{
		RequestId: "req-1",
		Body:      &tunnelv1.GatewayFrame_Cancel{Cancel: &tunnelv1.Cancel{}},
	})

	select {
	case err := <-writeErr:
		if err == nil {
			t.Fatal("writing to a cancelled request succeeded")
		}
		var re *runtime.RuntimeError
		if !errors.As(err, &re) || re.Code != runtime.ErrorConnection {
			t.Errorf("write after cancel returned %v, want a %s RuntimeError", err, runtime.ErrorConnection)
		}
	case <-time.After(testTimeout):
		t.Fatal("the handler never returned after its request was cancelled")
	}

	f.expectEnd(c, "req-1")
	f.expectReady(c)
}

func TestPoolStopCancelsInFlightRequests(t *testing.T) {
	f := newPoolFixture(t, soloSlot)

	done := make(chan error, 1)
	f.handler.set(func(ctx context.Context, _ *tunnel.Request, _ tunnel.ResponseSink) error {
		<-ctx.Done()
		done <- context.Cause(ctx)
		return ctx.Err()
	})
	f.start()

	c := f.acceptSlots(1)[0]
	f.dispatch(c, "req-1")
	f.handler.waitCall(t)
	if got := f.pool.InFlight(); got != 1 {
		t.Fatalf("in-flight requests = %d, want %d", got, 1)
	}

	// Stopping voids every slot of this connection. In-flight requests end
	// with a definite error instead of being silently resumed elsewhere.
	f.pool.Stop()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("Stop did not cancel the in-flight request")
	}
	if got := f.pool.InFlight(); got != 0 {
		t.Errorf("in-flight requests after Stop = %d, want %d", got, 0)
	}
	f.expectClosed(c)
}

// contains reports whether needle appears in s, without pulling in strings
// just for one assertion's sake.
func contains(s, needle string) bool {
	if len(needle) > len(s) {
		return false
	}
	for i := 0; i+len(needle) <= len(s); i++ {
		if s[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
