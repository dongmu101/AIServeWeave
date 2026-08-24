package scheduler_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// chatCapableSnapshot describes one runtime instance that supports chat,
// chat streaming and embeddings for model.
func chatCapableSnapshot(runtimeID, model string) runtime.Snapshot {
	return runtime.Snapshot{
		Descriptor: runtime.Descriptor{ID: runtimeID, Kind: runtime.KindOllama, BaseURL: "http://127.0.0.1:11434", MaxConcurrent: 4},
		State:      runtime.StateHealthy,
		Discovery: runtime.Discovery{
			Models: []runtime.Model{{
				ID: model,
				Capabilities: runtime.CapabilitySet{
					runtime.CapabilityChat:       {Level: runtime.SupportSupported},
					runtime.CapabilityChatStream: {Level: runtime.SupportSupported},
					runtime.CapabilityEmbeddings: {Level: runtime.SupportSupported},
				},
			}},
		},
	}
}

// connectNode wires up nodeID with a Control stream reporting snap and
// slots parked equal to len(handlers), each answered by the corresponding
// handler.
func connectNode(t *testing.T, h *gatewaytest.Harness, nodeID, runtimeID string, snap runtime.Snapshot, handlers ...gatewaytest.SlotHandler) {
	t.Helper()
	c := h.Connect(nodeID, runtimeID)
	c.Send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Status{Status: &tunnelv1.RuntimeStatus{
		Full:       true,
		ReportedAt: timestamppb.New(h.Clock.Now()),
		Snapshots:  tunnelwire.SnapshotsToProto([]runtime.Snapshot{snap}),
	}}})
	for i, handle := range handlers {
		h.OpenSlot(nodeID, tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, fmt.Sprintf("%s-slot-%d", nodeID, i), handle)
	}
	gatewaytest.WaitFor(t, "slots to park on "+nodeID, func() bool {
		return gatewaytest.IdleCount(h, nodeID) == len(handlers)
	})
	gatewaytest.WaitFor(t, "inventory to arrive on "+nodeID, func() bool {
		info, _ := h.Srv.Node(nodeID)
		return len(info.Runtimes) == 1
	})
}

// chatHandler answers Chat, ChatStream and Embed with a response tagged
// with source, so a test can tell which node actually served the request.
// count, if non-nil, is incremented once per request this handler answers.
func chatHandler(source string, count *atomic.Int32) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		if count != nil {
			count.Add(1)
		}
		switch req.GetOperation() {
		case tunnelv1.Operation_OPERATION_CHAT:
			payload, err := tunnelwire.MarshalChatResponse(runtime.ChatResponse{
				ID:           "chat-1",
				Message:      runtime.ChatMessage{Role: "assistant", Content: "served by " + source},
				FinishReason: "stop",
			})
			if err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame(payload))
		case tunnelv1.Operation_OPERATION_CHAT_STREAM:
			payload, err := tunnelwire.MarshalChatEvent(runtime.ChatEvent{
				ID:    "chat-1",
				Delta: runtime.ChatMessageDelta{Role: "assistant", Content: "served by " + source},
			})
			if err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame(payload))
		case tunnelv1.Operation_OPERATION_EMBED:
			payload, err := tunnelwire.MarshalEmbeddingResponse(runtime.EmbeddingResponse{
				Data: []runtime.Embedding{{Index: 0, Vector: []float32{1}}},
			})
			if err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame(payload))
		}
		return errors.New("unsupported operation")
	}
}

// failingHandler always answers with a retryable wire error, simulating a
// node whose Control is up but whose backend rejects Serve — the
// "接受 Control 但拒绝 Serve" fault tunnel/README.md phase 7 lists.
func failingHandler(count *atomic.Int32) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		if count != nil {
			count.Add(1)
		}
		return &gatewaytest.WireError{Code: "upstream_error", Message: "backend down", Retryable: true}
	}
}

// nonRetryableHandler always answers with a non-retryable wire error.
func nonRetryableHandler(count *atomic.Int32) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		if count != nil {
			count.Add(1)
		}
		return &gatewaytest.WireError{Code: "protocol_error", Message: "bad request", Retryable: false}
	}
}

// streamThenFailHandler delivers one ChatEvent, then ends the stream with a
// retryable wire error — simulating the backend dying mid-stream, after the
// response has already been committed to the caller.
func streamThenFailHandler(count *atomic.Int32) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		if count != nil {
			count.Add(1)
		}
		payload, err := tunnelwire.MarshalChatEvent(runtime.ChatEvent{
			ID:    "chat-1",
			Delta: runtime.ChatMessageDelta{Role: "assistant", Content: "first"},
		})
		if err != nil {
			return err
		}
		if err := reply(gatewaytest.DataFrame(payload)); err != nil {
			return err
		}
		return &gatewaytest.WireError{Code: "upstream_error", Message: "backend died mid-stream", Retryable: true}
	}
}

func TestChatPicksTheMoreIdleNode(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var lessIdleCount, moreIdleCount atomic.Int32
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler("node-a", &lessIdleCount))
	connectNode(t, h, "node-b", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"),
		chatHandler("node-b", &moreIdleCount), chatHandler("node-b", &moreIdleCount))

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	resp, candidate, err := sched.Chat(context.Background(), runtime.ChatRequest{
		Model:    "qwen3:8b",
		Messages: []runtime.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if candidate.NodeID != "node-b" {
		t.Errorf("candidate.NodeID = %q, want node-b (2 idle slots vs 1)", candidate.NodeID)
	}
	if resp.Message.Content != "served by node-b" {
		t.Errorf("content = %q, want %q", resp.Message.Content, "served by node-b")
	}
	if lessIdleCount.Load() != 0 {
		t.Errorf("the less-idle node was contacted %d times, want 0", lessIdleCount.Load())
	}
}

func TestChatReturnsErrNoCapableNodeForAnUnknownModel(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler("node-a", nil))

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	_, _, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "does-not-exist"})
	if !errors.Is(err, scheduler.ErrNoCapableNode) {
		t.Errorf("err = %v, want ErrNoCapableNode", err)
	}
}

func TestChatRetriesOnARetryableFailure(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var failingCount, workingCount atomic.Int32
	// node-a has more idle capacity, so it is tried first and fails.
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"),
		failingHandler(&failingCount), failingHandler(&failingCount))
	connectNode(t, h, "node-b", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler("node-b", &workingCount))

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	resp, candidate, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if candidate.NodeID != "node-b" || resp.Message.Content != "served by node-b" {
		t.Errorf("Chat did not fail over to node-b: candidate=%+v content=%q", candidate, resp.Message.Content)
	}
	if failingCount.Load() != 1 {
		t.Errorf("the failing node was contacted %d times, want 1", failingCount.Load())
	}
}

func TestChatDoesNotRetryANonRetryableFailure(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var failingCount, workingCount atomic.Int32
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"),
		nonRetryableHandler(&failingCount), nonRetryableHandler(&failingCount))
	connectNode(t, h, "node-b", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler("node-b", &workingCount))

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	_, candidate, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"})
	if err == nil {
		t.Fatal("Chat succeeded, want the non-retryable error")
	}
	if candidate.NodeID != "node-a" {
		t.Errorf("candidate.NodeID = %q, want node-a", candidate.NodeID)
	}
	if workingCount.Load() != 0 {
		t.Errorf("node-b was contacted %d times after a non-retryable failure, want 0", workingCount.Load())
	}
}

func TestChatStreamRetriesBeforeTheFirstEventButNotAfter(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var failingCount, workingCount atomic.Int32
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"),
		failingHandler(&failingCount), failingHandler(&failingCount))
	connectNode(t, h, "node-b", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), streamThenFailHandler(&workingCount))

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	stream, candidate, err := sched.ChatStream(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	defer stream.Close()
	if candidate.NodeID != "node-b" {
		t.Fatalf("candidate.NodeID = %q, want node-b (node-a should have failed and been retried)", candidate.NodeID)
	}
	if failingCount.Load() != 1 {
		t.Errorf("node-a was contacted %d times, want 1", failingCount.Load())
	}

	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("first Recv: %v", err)
	}
	if first.Delta.Content != "first" {
		t.Errorf("first event content = %q, want %q", first.Delta.Content, "first")
	}
	if !stream.Committed() {
		t.Error("Committed() is false after the first event was delivered")
	}

	if _, err := stream.Recv(); err == nil {
		t.Error("second Recv succeeded, want the scripted mid-stream failure")
	}
	if workingCount.Load() != 1 {
		t.Errorf("node-b was contacted %d times, want 1 (no retry once committed)", workingCount.Load())
	}
}

func TestChatStreamEmptyResponseIsNotAnError(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"),
		func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
			return nil
		})

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	stream, _, err := sched.ChatStream(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	defer stream.Close()
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Errorf("Recv = %v, want io.EOF", err)
	}
}

func TestEmbedDispatches(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "nomic-embed"), chatHandler("node-a", nil))

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	resp, candidate, err := sched.Embed(context.Background(), runtime.EmbeddingRequest{Model: "nomic-embed", Input: []string{"x"}})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if candidate.NodeID != "node-a" {
		t.Errorf("candidate.NodeID = %q, want node-a", candidate.NodeID)
	}
	if len(resp.Data) != 1 {
		t.Errorf("Data = %v, want one embedding", resp.Data)
	}
}

func TestModelsAggregatesAcrossNodesAndDeduplicates(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler("node-a", nil))
	connectNode(t, h, "node-b", "backend-1", chatCapableSnapshot("backend-1", "gemma3:27b"), chatHandler("node-b", nil))
	// A second node also serving qwen3:8b must not produce a duplicate entry.
	connectNode(t, h, "node-c", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler("node-c", nil))

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	models := sched.Models(context.Background())
	if len(models) != 2 {
		t.Fatalf("Models = %v, want 2 distinct models", models)
	}
	if models[0].ID != "gemma3:27b" || models[1].ID != "qwen3:8b" {
		t.Errorf("Models = %v, want [gemma3:27b qwen3:8b] sorted", models)
	}
}

// flakyThenWorkingHandler answers the first failThreshold requests with a
// retryable upstream error, then answers every request after that as a
// normal chatHandler would — simulating a node that comes back after being
// briefly broken.
func flakyThenWorkingHandler(source string, failThreshold int32, count *atomic.Int32) gatewaytest.SlotHandler {
	working := chatHandler(source, nil)
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		n := count.Add(1)
		if n <= failThreshold {
			return &gatewaytest.WireError{Code: "upstream_error", Message: "backend down", Retryable: true}
		}
		return working(req, body, reply)
	}
}

// backpressureHandler always answers with a backpressure wire error, the
// congestion signal that must never trip a breaker.
func backpressureHandler(count *atomic.Int32) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		if count != nil {
			count.Add(1)
		}
		return &gatewaytest.WireError{Code: "backpressure", Message: "no idle slot", Retryable: true}
	}
}

func TestChatSkipsANodeAfterItsBreakerTrips(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var failCount atomic.Int32
	// A single node so the breaker's effect is unambiguous: once it trips,
	// there is nothing else candidates() could have picked instead.
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), failingHandler(&failCount))

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock, FailureThreshold: 2})
	for i := range 2 {
		_, _, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"})
		if err == nil {
			t.Fatalf("call %d: Chat succeeded, want the scripted upstream_error", i+1)
		}
	}
	if got := failCount.Load(); got != 2 {
		t.Fatalf("node-a was contacted %d times before the trip, want exactly 2", got)
	}

	_, _, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"})
	if !errors.Is(err, scheduler.ErrNoCapableNode) {
		t.Fatalf("Chat after the trip = %v, want ErrNoCapableNode (the only candidate should be excluded)", err)
	}
	if got := failCount.Load(); got != 2 {
		t.Errorf("node-a was contacted again after tripping (count=%d), want it skipped entirely", got)
	}
}

func TestChatRecoversAfterTheCooldownOnceAProbeSucceeds(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var count atomic.Int32
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"),
		flakyThenWorkingHandler("node-a", 2, &count))

	sched := scheduler.New(h.Srv, scheduler.Config{
		Clock: h.Clock, FailureThreshold: 2, BaseCooldown: 10 * time.Second, MaxCooldown: time.Minute,
	})
	// The node has one slot, which re-parks asynchronously after each reply;
	// wait for it between sequential calls so a call never races ahead and
	// sees a spurious "no idle slot" instead of what the breaker decided.
	waitForSlot := func() {
		t.Helper()
		gatewaytest.WaitFor(t, "node-a's slot to re-park", func() bool { return gatewaytest.IdleCount(h, "node-a") == 1 })
	}
	waitForSlot()
	for range 2 {
		if _, _, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"}); err == nil {
			t.Fatal("Chat succeeded before the handler was scripted to recover")
		}
		waitForSlot()
	}

	// Breaker is open now; too early to probe.
	if _, _, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"}); !errors.Is(err, scheduler.ErrNoCapableNode) {
		t.Fatalf("Chat during the cooldown = %v, want ErrNoCapableNode", err)
	}

	h.Clock.Advance(10 * time.Second)
	resp, candidate, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"})
	if err != nil {
		t.Fatalf("Chat after the cooldown elapsed: %v", err)
	}
	if candidate.NodeID != "node-a" || resp.Message.Content != "served by node-a" {
		t.Errorf("recovered call did not reach node-a: candidate=%+v content=%q", candidate, resp.Message.Content)
	}
	waitForSlot()

	// Fully recovered: further calls must not be excluded again.
	if _, _, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"}); err != nil {
		t.Errorf("Chat after recovery: %v, want success", err)
	}
}

func TestChatBackpressureNeverTripsTheBreaker(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var count atomic.Int32
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), backpressureHandler(&count))

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock, FailureThreshold: 2})
	for i := range 10 {
		_, _, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"})
		var rtErr *runtime.RuntimeError
		if !errors.As(err, &rtErr) || rtErr.Code != runtime.ErrorBackpressure {
			t.Fatalf("call %d: err = %v, want a backpressure RuntimeError (breaker must never intercept it)", i+1, err)
		}
	}
	if got := count.Load(); got != 10 {
		t.Errorf("node-a was contacted %d times, want all 10 (backpressure must never exclude the node)", got)
	}
}

func TestCandidatesExcludeAnUnhealthyRuntimeAndReadmitItOnRecovery(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var count atomic.Int32
	unhealthy := chatCapableSnapshot("backend-1", "qwen3:8b")
	unhealthy.State = runtime.StateUnhealthy

	c := h.Connect("node-a", "backend-1")
	c.Send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Status{Status: &tunnelv1.RuntimeStatus{
		Full:       true,
		ReportedAt: timestamppb.New(h.Clock.Now()),
		Snapshots:  tunnelwire.SnapshotsToProto([]runtime.Snapshot{unhealthy}),
	}}})
	h.OpenSlot("node-a", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "node-a-slot-0", chatHandler("node-a", &count))
	gatewaytest.WaitFor(t, "slot to park on node-a", func() bool { return gatewaytest.IdleCount(h, "node-a") == 1 })
	gatewaytest.WaitFor(t, "inventory to arrive on node-a", func() bool {
		info, _ := h.Srv.Node("node-a")
		return len(info.Runtimes) == 1
	})

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	if _, _, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"}); !errors.Is(err, scheduler.ErrNoCapableNode) {
		t.Fatalf("Chat against an unhealthy runtime = %v, want ErrNoCapableNode", err)
	}
	if got := count.Load(); got != 0 {
		t.Errorf("the unhealthy node was contacted %d times, want 0", got)
	}

	healthy := chatCapableSnapshot("backend-1", "qwen3:8b")
	c.Send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Status{Status: &tunnelv1.RuntimeStatus{
		Full:       true,
		ReportedAt: timestamppb.New(h.Clock.Now()),
		Snapshots:  tunnelwire.SnapshotsToProto([]runtime.Snapshot{healthy}),
	}}})
	gatewaytest.WaitFor(t, "node-a to report healthy", func() bool {
		info, _ := h.Srv.Node("node-a")
		return len(info.Runtimes) == 1 && info.Runtimes[0].State == runtime.StateHealthy
	})

	resp, candidate, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"})
	if err != nil {
		t.Fatalf("Chat after recovery: %v", err)
	}
	if candidate.NodeID != "node-a" || resp.Message.Content != "served by node-a" {
		t.Errorf("recovered call did not reach node-a: candidate=%+v content=%q", candidate, resp.Message.Content)
	}
}
