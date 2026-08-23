package tunnelserver_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

func TestDispatchRefusesWhatItCannotRoute(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	h.connect("mac-mini-01")

	tests := []struct {
		name      string
		req       tunnelserver.Request
		wantCode  runtime.ErrorCode
		wantCause error
		// wantRetryable states whether a scheduler may place this request on
		// another node. It is asserted separately from the code because the
		// two answer different questions and have drifted apart before.
		wantRetryable bool
	}{
		{
			name:          "unknown node",
			req:           tunnelserver.Request{NodeID: "gpu-box-07", RuntimeID: "ollama-1"},
			wantCode:      runtime.ErrorConnection,
			wantCause:     tunnelserver.ErrNodeNotConnected,
			wantRetryable: true,
		},
		{
			name:          "connected node with no parked slot",
			req:           tunnelserver.Request{NodeID: "mac-mini-01", RuntimeID: "ollama-1"},
			wantCode:      runtime.ErrorBackpressure,
			wantCause:     tunnelserver.ErrNoIdleSlot,
			wantRetryable: true,
		},
		{
			name:          "no runtime_id",
			req:           tunnelserver.Request{NodeID: "mac-mini-01"},
			wantCode:      runtime.ErrorInvalidConfig,
			wantRetryable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.srv.Dispatch(context.Background(), tt.req)
			var rtErr *runtime.RuntimeError
			if !errors.As(err, &rtErr) {
				t.Fatalf("Dispatch error = %v (%T), want a *runtime.RuntimeError", err, err)
			}
			if rtErr.Code != tt.wantCode {
				t.Errorf("Code = %s, want %s", rtErr.Code, tt.wantCode)
			}
			if tt.wantCause != nil && !errors.Is(err, tt.wantCause) {
				t.Errorf("error %v does not wrap %v", err, tt.wantCause)
			}
			if rtErr.Retryable != tt.wantRetryable {
				t.Errorf("Retryable = %t, want %t", rtErr.Retryable, tt.wantRetryable)
			}
		})
	}
}

func TestDispatchReturnsBackpressureImmediatelyRatherThanQueueing(t *testing.T) {
	// The point of parking slots in advance is that "this node is full" is a
	// microsecond answer. If Dispatch ever queued, a scheduler could not tell
	// a busy node from a slow one, and the caller would wait on capacity this
	// replica cannot see the end of.
	h := newHarness(t, tunnelserver.Config{})
	h.connect("mac-mini-01")

	release := make(chan struct{})
	defer close(release)
	h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-1",
		func(req *tunnelv1.RequestHeaders, _ [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
			<-release
			return nil
		})
	waitFor(t, "the slot to park", func() bool { return idleCount(h, "mac-mini-01") == 1 })

	req := tunnelserver.Request{
		NodeID:    "mac-mini-01",
		RuntimeID: "ollama-1",
		Operation: tunnelv1.Operation_OPERATION_CHAT,
	}
	first, err := h.srv.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	defer first.Close()

	start := time.Now()
	if _, err := h.srv.Dispatch(context.Background(), req); !errors.Is(err, tunnelserver.ErrNoIdleSlot) {
		t.Fatalf("second dispatch = %v, want backpressure", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("backpressure took %v to report; it must be immediate, not a wait for a slot", elapsed)
	}
}

func TestDispatchRoundTripsASingleResponse(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	h.connect("mac-mini-01")
	h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-1", echoHandler)
	waitFor(t, "the slot to park", func() bool { return idleCount(h, "mac-mini-01") == 1 })

	resp, err := h.srv.Dispatch(context.Background(), tunnelserver.Request{
		NodeID:    "mac-mini-01",
		RuntimeID: "ollama-1",
		Operation: tunnelv1.Operation_OPERATION_CHAT,
		Payload:   []byte("request-payload"),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	defer resp.Close()

	got, err := resp.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if string(got) != "request-payload" {
		t.Errorf("payload = %q, want %q", got, "request-payload")
	}
	if _, err := resp.Recv(); err != io.EOF {
		t.Errorf("Recv after the payload = %v, want io.EOF", err)
	}
}

func TestDispatchPassesRuntimeIDOperationAndDeadlineThrough(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	h.connect("mac-mini-01")

	seen := make(chan *tunnelv1.RequestHeaders, 1)
	h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-1",
		func(req *tunnelv1.RequestHeaders, _ [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
			seen <- req
			return nil
		})
	waitFor(t, "the slot to park", func() bool { return idleCount(h, "mac-mini-01") == 1 })

	deadline := time.Now().Add(30 * time.Second).Truncate(time.Millisecond)
	resp, err := h.srv.Dispatch(context.Background(), tunnelserver.Request{
		NodeID:    "mac-mini-01",
		RuntimeID: "ollama-1",
		Operation: tunnelv1.Operation_OPERATION_EMBED,
		Deadline:  deadline,
		Trace:     map[string]string{"request_id": "req-42"},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	defer resp.Close()

	headers := <-seen
	if headers.GetRuntimeId() != "ollama-1" {
		t.Errorf("runtime_id = %q, want ollama-1", headers.GetRuntimeId())
	}
	if headers.GetOperation() != tunnelv1.Operation_OPERATION_EMBED {
		t.Errorf("operation = %v, want OPERATION_EMBED", headers.GetOperation())
	}
	if got := headers.GetDeadlineUnixMs(); got != deadline.UnixMilli() {
		t.Errorf("deadline_unix_ms = %d, want %d", got, deadline.UnixMilli())
	}
	if got := headers.GetTrace()["request_id"]; got != "req-42" {
		t.Errorf("trace[request_id] = %q, want req-42", got)
	}
}

func TestDispatchTakesTheEarlierOfTheRequestAndContextDeadlines(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	h.connect("mac-mini-01")
	seen := make(chan *tunnelv1.RequestHeaders, 1)
	h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-1",
		func(req *tunnelv1.RequestHeaders, _ [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
			seen <- req
			return nil
		})
	waitFor(t, "the slot to park", func() bool { return idleCount(h, "mac-mini-01") == 1 })

	// The caller's own deadline is the tighter one; sending the looser
	// request deadline would leave the Agent working past the point where
	// anybody is still listening.
	ctxDeadline := time.Now().Add(5 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), ctxDeadline)
	defer cancel()

	resp, err := h.srv.Dispatch(ctx, tunnelserver.Request{
		NodeID:    "mac-mini-01",
		RuntimeID: "ollama-1",
		Operation: tunnelv1.Operation_OPERATION_CHAT,
		Deadline:  time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	defer resp.Close()

	if got := (<-seen).GetDeadlineUnixMs(); got != ctxDeadline.UnixMilli() {
		t.Errorf("deadline_unix_ms = %d, want the context's %d", got, ctxDeadline.UnixMilli())
	}
}

func TestDispatchDeliversEachStreamEventSeparately(t *testing.T) {
	// Aggregating events would turn a millisecond first token into a whole
	// response's worth of latency, so the test asserts arrival order and
	// count, not just the concatenation.
	h := newHarness(t, tunnelserver.Config{})
	h.connect("mac-mini-01")

	events := []string{"Hel", "lo,", " wo", "rld"}
	h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-1",
		func(_ *tunnelv1.RequestHeaders, _ [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
			for _, ev := range events {
				if err := reply(dataFrame([]byte(ev))); err != nil {
					return err
				}
			}
			return nil
		})
	waitFor(t, "the slot to park", func() bool { return idleCount(h, "mac-mini-01") == 1 })

	resp, err := h.srv.Dispatch(context.Background(), tunnelserver.Request{
		NodeID:    "mac-mini-01",
		RuntimeID: "ollama-1",
		Operation: tunnelv1.Operation_OPERATION_CHAT_STREAM,
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	defer resp.Close()

	var got []string
	for {
		chunk, err := resp.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		got = append(got, string(chunk))
	}
	if strings.Join(got, "|") != strings.Join(events, "|") {
		t.Errorf("events = %v, want %v", got, events)
	}
}

func TestDispatchReusesASlotAfterTheAgentReparksIt(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	h.connect("mac-mini-01")
	h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-1", echoHandler)

	for i := range 3 {
		waitFor(t, fmt.Sprintf("the slot to park before request %d", i), func() bool {
			return idleCount(h, "mac-mini-01") == 1
		})
		payload := fmt.Sprintf("request-%d", i)
		resp, err := h.srv.Dispatch(context.Background(), tunnelserver.Request{
			NodeID:    "mac-mini-01",
			RuntimeID: "ollama-1",
			Operation: tunnelv1.Operation_OPERATION_CHAT,
			Payload:   []byte(payload),
		})
		if err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
		got, err := resp.Recv()
		if err != nil {
			t.Fatalf("recv %d: %v", i, err)
		}
		if string(got) != payload {
			t.Errorf("request %d answered with %q, wanted %q: a reused slot mixed up requests", i, got, payload)
		}
		if _, err := resp.Recv(); err != io.EOF {
			t.Fatalf("recv %d end = %v, want io.EOF", i, err)
		}
		resp.Close()
	}
}

func TestDispatchRestoresTheAgentsErrorCodeAndRetryableFlag(t *testing.T) {
	// The whole point of TunnelError is that a runtime error survives the
	// crossing: a Gateway scheduler branches on the code and the flag, and a
	// tunnel that flattened them into "something went wrong" would make every
	// failure look alike.
	h := newHarness(t, tunnelserver.Config{})
	h.connect("mac-mini-01")

	want := &wireError{
		code:      string(runtime.ErrorRateLimited),
		message:   "backend said 429",
		retryable: true,
		cause:     "ErrConcurrencyLimit",
	}
	h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-1",
		func(*tunnelv1.RequestHeaders, [][]byte, func(*tunnelv1.AgentFrame) error) error { return want })
	waitFor(t, "the slot to park", func() bool { return idleCount(h, "mac-mini-01") == 1 })

	resp, err := h.srv.Dispatch(context.Background(), tunnelserver.Request{
		NodeID:    "mac-mini-01",
		RuntimeID: "ollama-1",
		Operation: tunnelv1.Operation_OPERATION_CHAT,
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	defer resp.Close()

	_, err = resp.Recv()
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) {
		t.Fatalf("Recv error = %v (%T), want a *runtime.RuntimeError", err, err)
	}
	if rtErr.Code != runtime.ErrorRateLimited {
		t.Errorf("Code = %s, want %s", rtErr.Code, runtime.ErrorRateLimited)
	}
	if !rtErr.Retryable {
		t.Error("Retryable = false, want true as the Agent reported it")
	}
	if !errors.Is(err, runtime.ErrConcurrencyLimit) {
		t.Errorf("error %v does not restore the ErrConcurrencyLimit sentinel", err)
	}
}

func TestDispatchAbortsInFlightRequestsWhenTheSlotDies(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	h.connect("mac-mini-01")

	entered := make(chan struct{})
	block := make(chan struct{})
	defer close(block)
	slot := h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-1",
		func(*tunnelv1.RequestHeaders, [][]byte, func(*tunnelv1.AgentFrame) error) error {
			close(entered)
			<-block
			return nil
		})
	waitFor(t, "the slot to park", func() bool { return idleCount(h, "mac-mini-01") == 1 })

	resp, err := h.srv.Dispatch(context.Background(), tunnelserver.Request{
		NodeID:    "mac-mini-01",
		RuntimeID: "ollama-1",
		Operation: tunnelv1.Operation_OPERATION_CHAT_STREAM,
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	defer resp.Close()
	<-entered

	slot.stream.Break(errors.New("connection reset"))

	_, err = resp.Recv()
	if err == nil || err == io.EOF {
		t.Fatalf("Recv after the slot died = %v, want a failure: a dead link must never look like a clean end", err)
	}
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) {
		t.Fatalf("Recv error = %v (%T), want a *runtime.RuntimeError", err, err)
	}
	if rtErr.Code != runtime.ErrorConnection {
		t.Errorf("Code = %s, want %s", rtErr.Code, runtime.ErrorConnection)
	}
	if rtErr.Retryable {
		t.Error("Retryable = true; this replica cannot know whether tokens already reached the caller, so it must not invite a retry")
	}
}

func TestDispatchCancelsTheAgentWhenTheCallerStopsReading(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	h.connect("mac-mini-01")

	// This slot is driven by hand rather than by the harness loop, so the
	// test can watch for the Cancel frame itself.
	slot := h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-1", nil)
	waitFor(t, "the slot to park", func() bool { return idleCount(h, "mac-mini-01") == 1 })

	resp, err := h.srv.Dispatch(context.Background(), tunnelserver.Request{
		NodeID:    "mac-mini-01",
		RuntimeID: "ollama-1",
		Operation: tunnelv1.Operation_OPERATION_CHAT_STREAM,
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	headers, err := slot.stream.ToAgent(ctx)
	if err != nil {
		t.Fatalf("waiting for RequestHeaders: %v", err)
	}
	requestID := headers.GetRequestId()
	if _, err := slot.stream.ToAgent(ctx); err != nil { // RequestEnd
		t.Fatalf("waiting for RequestEnd: %v", err)
	}

	resp.Close()

	frame, err := slot.stream.ToAgent(ctx)
	if err != nil {
		t.Fatalf("waiting for Cancel: %v", err)
	}
	if frame.GetCancel() == nil {
		t.Fatalf("frame after Close = %T, want Cancel", frame.GetBody())
	}
	if frame.GetRequestId() != requestID {
		t.Errorf("Cancel.request_id = %q, want %q: a cancel that names the wrong request would kill whatever reused the slot",
			frame.GetRequestId(), requestID)
	}
}

func TestDispatchSendsRequestEndForOperationsWithNoBody(t *testing.T) {
	// Every operation gets a RequestEnd, including those whose whole request
	// fit in the headers, so the Agent has one uniform "no more input" signal
	// instead of a per-operation rule.
	h := newHarness(t, tunnelserver.Config{})
	h.connect("mac-mini-01")
	slot := h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-1", nil)
	waitFor(t, "the slot to park", func() bool { return idleCount(h, "mac-mini-01") == 1 })

	resp, err := h.srv.Dispatch(context.Background(), tunnelserver.Request{
		NodeID:    "mac-mini-01",
		RuntimeID: "ollama-1",
		Operation: tunnelv1.Operation_OPERATION_LIST_MODELS,
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	defer resp.Close()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if frame, err := slot.stream.ToAgent(ctx); err != nil || frame.GetHeaders() == nil {
		t.Fatalf("first frame = %v (err %v), want RequestHeaders", frame, err)
	}
	frame, err := slot.stream.ToAgent(ctx)
	if err != nil {
		t.Fatalf("waiting for RequestEnd: %v", err)
	}
	if frame.GetEnd() == nil {
		t.Fatalf("second frame = %T, want RequestEnd", frame.GetBody())
	}
}

func TestDispatchStreamsARequestBodyInChunks(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	h.connect("mac-mini-01")

	got := make(chan [][]byte, 1)
	h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-1",
		func(_ *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
			got <- body
			return nil
		})
	waitFor(t, "the slot to park", func() bool { return idleCount(h, "mac-mini-01") == 1 })

	chunks := [][]byte{[]byte(`{"nodes":`), []byte(`{}}`)}
	resp, err := h.srv.Dispatch(context.Background(), tunnelserver.Request{
		NodeID:    "mac-mini-01",
		RuntimeID: "comfy-1",
		Operation: tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT,
		Body: func(send func([]byte) error) error {
			for _, chunk := range chunks {
				if err := send(chunk); err != nil {
					return err
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	defer resp.Close()

	body := <-got
	if len(body) != len(chunks) {
		t.Fatalf("received %d body chunks, want %d: chunks must not be merged or split on the way", len(body), len(chunks))
	}
	for i := range chunks {
		if string(body[i]) != string(chunks[i]) {
			t.Errorf("chunk %d = %q, want %q", i, body[i], chunks[i])
		}
	}
}

func TestDispatchSendsArtifactsToABulkSlot(t *testing.T) {
	// A multi-hundred-megabyte download must not occupy an inference slot:
	// that is the whole reason the bulk class exists.
	h := newHarness(t, tunnelserver.Config{})
	h.connect("mac-mini-01")
	h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "inference-1", echoHandler)
	h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_BULK, "bulk-1", echoHandler)
	waitFor(t, "both slots to park", func() bool { return idleCount(h, "mac-mini-01") == 2 })

	resp, err := h.srv.Dispatch(context.Background(), tunnelserver.Request{
		NodeID:    "mac-mini-01",
		RuntimeID: "comfy-1",
		Operation: tunnelv1.Operation_OPERATION_ARTIFACT_OPEN,
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	defer resp.Close()

	info, _ := h.srv.Node("mac-mini-01")
	if info.IdleSlots[tunnelv1.SlotClass_SLOT_CLASS_BULK] != 0 {
		t.Error("the bulk slot is still idle; the artifact went somewhere else")
	}
	if info.IdleSlots[tunnelv1.SlotClass_SLOT_CLASS_INFERENCE] != 1 {
		t.Error("the inference slot was consumed by an artifact download")
	}
}

func TestDispatchRejectsAnOversizedResponseChunk(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{MaxFrameBytes: 64})
	h.connect("mac-mini-01")
	slot := h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-1",
		func(_ *tunnelv1.RequestHeaders, _ [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
			return reply(dataFrame(make([]byte, 65)))
		})
	waitFor(t, "the slot to park", func() bool { return idleCount(h, "mac-mini-01") == 1 })

	resp, err := h.srv.Dispatch(context.Background(), tunnelserver.Request{
		NodeID:    "mac-mini-01",
		RuntimeID: "ollama-1",
		Operation: tunnelv1.Operation_OPERATION_CHAT,
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	defer resp.Close()

	if _, err := resp.Recv(); err == nil || err == io.EOF {
		t.Fatalf("Recv = %v, want a failure for an oversized chunk", err)
	}
	if err := slot.wait(t); err == nil {
		t.Error("the slot stayed open after a framing violation; a slot whose framing is not understood cannot be safely reused")
	}
}

func TestDispatchSurvivesConcurrentRequestsAcrossSlots(t *testing.T) {
	const slots = 8
	h := newHarness(t, tunnelserver.Config{})
	h.connect("mac-mini-01")
	for i := range slots {
		h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, fmt.Sprintf("slot-%d", i), echoHandler)
	}
	waitFor(t, "every slot to park", func() bool { return idleCount(h, "mac-mini-01") == slots })

	var wg sync.WaitGroup
	errs := make(chan error, slots)
	for i := range slots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload := fmt.Sprintf("payload-%d", i)
			resp, err := h.srv.Dispatch(context.Background(), tunnelserver.Request{
				NodeID:    "mac-mini-01",
				RuntimeID: "ollama-1",
				Operation: tunnelv1.Operation_OPERATION_CHAT,
				Payload:   []byte(payload),
			})
			if err != nil {
				errs <- err
				return
			}
			defer resp.Close()
			got, err := resp.Recv()
			if err != nil {
				errs <- err
				return
			}
			if string(got) != payload {
				errs <- fmt.Errorf("got %q, want %q: responses were routed to the wrong caller", got, payload)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
