package tunnelserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// runtimeHarness is a harness with one connected node, one parked slot, and a
// scripted Agent that answers each operation with the response type its
// payload contract names.
func newRuntimeHarness(t *testing.T) (*harness, *tunnelserver.NodeRuntime) {
	t.Helper()
	h := newHarness(t, tunnelserver.Config{})
	c := h.connect("mac-mini-01", "ollama-1")

	c.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Status{Status: &tunnelv1.RuntimeStatus{
		Full:       true,
		ReportedAt: timestamppb.New(h.clock.Now()),
		Snapshots: tunnelwire.SnapshotsToProto([]runtime.Snapshot{{
			Descriptor: runtime.Descriptor{ID: "ollama-1", Kind: runtime.KindOllama, BaseURL: "http://127.0.0.1:11434", MaxConcurrent: 4},
			State:      runtime.StateHealthy,
			Health:     runtime.HealthReport{State: runtime.StateHealthy},
			Probe:      runtime.ProbeResult{Kind: runtime.KindOllama, Version: "0.5.0", IdentityVerified: true},
			Discovery: runtime.Discovery{
				Version: "0.5.0",
				Models:  []runtime.Model{{ID: "qwen3:8b"}},
			},
		}}),
	}}})

	h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-1", runtimeAgent)
	h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_BULK, "bulk-1", runtimeAgent)
	waitFor(t, "both slots to park", func() bool { return idleCount(h, "mac-mini-01") == 2 })
	waitFor(t, "the inventory to arrive", func() bool {
		info, _ := h.srv.Node("mac-mini-01")
		return len(info.Runtimes) == 1
	})

	return h, h.srv.Runtime("mac-mini-01", "ollama-1")
}

// runtimeAgent plays an Agent that answers each operation with a canned
// response encoded by the shared codec — the same codec the Gateway decodes
// with, which is exactly the property that makes one tunnelwire package worth
// having.
func runtimeAgent(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
	switch req.GetOperation() {
	case tunnelv1.Operation_OPERATION_LIST_MODELS:
		payload, err := tunnelwire.MarshalModels([]runtime.Model{{ID: "qwen3:8b"}, {ID: "nomic-embed"}})
		if err != nil {
			return err
		}
		return reply(dataFrame(payload))

	case tunnelv1.Operation_OPERATION_CHAT:
		in, err := tunnelwire.UnmarshalChatRequest(req.GetPayload())
		if err != nil {
			return err
		}
		payload, err := tunnelwire.MarshalChatResponse(runtime.ChatResponse{
			ID:           "chat-1",
			Model:        in.Model,
			Message:      runtime.ChatMessage{Role: "assistant", Content: "answer to: " + in.Messages[0].Content},
			FinishReason: "stop",
			Usage:        runtime.Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8},
		})
		if err != nil {
			return err
		}
		return reply(dataFrame(payload))

	case tunnelv1.Operation_OPERATION_CHAT_STREAM:
		for _, delta := range []string{"Hel", "lo"} {
			payload, err := tunnelwire.MarshalChatEvent(runtime.ChatEvent{
				ID:    "chat-1",
				Model: "qwen3:8b",
				Delta: runtime.ChatMessageDelta{Role: "assistant", Content: delta},
			})
			if err != nil {
				return err
			}
			if err := reply(dataFrame(payload)); err != nil {
				return err
			}
		}
		return nil

	case tunnelv1.Operation_OPERATION_EMBED:
		payload, err := tunnelwire.MarshalEmbeddingResponse(runtime.EmbeddingResponse{
			Model: "nomic-embed",
			Data:  []runtime.Embedding{{Index: 0, Vector: []float32{0.5, -0.25}}},
		})
		if err != nil {
			return err
		}
		return reply(dataFrame(payload))

	case tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT:
		// The template travels in DataChunks, never in the headers: the
		// assertion that it arrived intact belongs here, on the Agent side.
		var joined []byte
		for _, chunk := range body {
			joined = append(joined, chunk...)
		}
		if !json.Valid(joined) {
			return errors.New("workflow template did not survive chunking")
		}
		payload, err := tunnelwire.MarshalWorkflowRun(runtime.WorkflowRun{ID: "prompt-1", RuntimeID: "comfy-1"})
		if err != nil {
			return err
		}
		return reply(dataFrame(payload))

	case tunnelv1.Operation_OPERATION_ARTIFACT_OPEN:
		if err := reply(&tunnelv1.AgentFrame{Body: &tunnelv1.AgentFrame_Headers{Headers: &tunnelv1.ResponseHeaders{
			ContentType: "image/png",
			Size:        6,
		}}}); err != nil {
			return err
		}
		for _, chunk := range [][]byte{[]byte("abc"), []byte("def")} {
			if err := reply(dataFrame(chunk)); err != nil {
				return err
			}
		}
		return nil
	}
	return errors.New("unsupported operation")
}

func TestNodeRuntimeSatisfiesTheRuntimeContractOverTheTunnel(t *testing.T) {
	h, rt := newRuntimeHarness(t)
	ctx := context.Background()

	// A slot re-parks asynchronously: the Agent sends Ready only after it has
	// finished the previous request, and this replica takes it back into the
	// idle set from the slot's own goroutine. These subtests run one after
	// another on the same two slots, so each has to wait for that to land —
	// otherwise it races the previous subtest's re-park and gets the
	// backpressure a genuinely full node would return.
	//
	// 槽是异步重新停放的：Agent 要等上一个请求结束后才发 Ready，而本副本是在槽自己的
	// 协程里把它收回空闲集合的。这些子测试前后相继地跑在同两个槽上，因此每个都必须等
	// 这件事落地——否则它就是在和上一个子测试的重新停放赛跑，然后拿到一个真正满载的
	// 节点才会返回的背压。
	awaitSlots := func(t *testing.T) {
		t.Helper()
		waitFor(t, "both slots to be parked", func() bool { return idleCount(h, "mac-mini-01") == 2 })
	}

	t.Run("ListModels", func(t *testing.T) {
		awaitSlots(t)
		models, err := rt.ListModels(ctx)
		if err != nil {
			t.Fatalf("ListModels: %v", err)
		}
		if len(models) != 2 || models[0].ID != "qwen3:8b" {
			t.Errorf("models = %v, want [qwen3:8b nomic-embed]", models)
		}
	})

	t.Run("Chat", func(t *testing.T) {
		awaitSlots(t)
		resp, err := rt.Chat(ctx, runtime.ChatRequest{
			Model:    "qwen3:8b",
			Messages: []runtime.ChatMessage{{Role: "user", Content: "hello"}},
		})
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if resp.Message.Content != "answer to: hello" {
			t.Errorf("content = %q, want %q", resp.Message.Content, "answer to: hello")
		}
		if resp.Usage.TotalTokens != 8 {
			t.Errorf("usage.total = %d, want 8", resp.Usage.TotalTokens)
		}
	})

	t.Run("Embed", func(t *testing.T) {
		awaitSlots(t)
		resp, err := rt.Embed(ctx, runtime.EmbeddingRequest{Model: "nomic-embed", Input: []string{"x"}})
		if err != nil {
			t.Fatalf("Embed: %v", err)
		}
		if len(resp.Data) != 1 || len(resp.Data[0].Vector) != 2 || resp.Data[0].Vector[1] != -0.25 {
			t.Errorf("embedding = %v, want one 2-dimensional vector ending in -0.25", resp.Data)
		}
	})

	t.Run("Submit", func(t *testing.T) {
		awaitSlots(t)
		run, err := rt.Submit(ctx, runtime.WorkflowRequest{
			Template: json.RawMessage(`{"3":{"class_type":"KSampler"}}`),
			ClientID: "client-1",
		})
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if run.ID != "prompt-1" {
			t.Errorf("run.ID = %q, want prompt-1", run.ID)
		}
	})
}

func TestNodeRuntimeChatStreamStaysUncommittedUntilTheFirstEvent(t *testing.T) {
	// Committed is the flag that forbids a transparent retry. It must flip on
	// the first event handed to the consumer and not before, or a scheduler
	// will either retry a request whose tokens are already downstream or
	// refuse to retry one that produced nothing.
	_, rt := newRuntimeHarness(t)

	stream, err := rt.ChatStream(context.Background(), runtime.ChatRequest{
		Model:    "qwen3:8b",
		Messages: []runtime.ChatMessage{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	defer stream.Close()

	if stream.Committed() {
		t.Error("Committed() is true before any event was delivered")
	}

	var content string
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if !stream.Committed() {
			t.Error("Committed() is false after an event was delivered")
		}
		content += ev.Delta.Content
	}
	if content != "Hello" {
		t.Errorf("streamed content = %q, want %q", content, "Hello")
	}
}

func TestNodeRuntimeChatStreamCloseIsIdempotentAndStopsRecv(t *testing.T) {
	_, rt := newRuntimeHarness(t)
	stream, err := rt.ChatStream(context.Background(), runtime.ChatRequest{
		Model:    "qwen3:8b",
		Messages: []runtime.ChatMessage{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := stream.Recv(); !errors.Is(err, runtime.ErrStreamClosed) {
		t.Errorf("Recv after Close = %v, want runtime.ErrStreamClosed", err)
	}
}

func TestNodeRuntimeOpenArtifactStreamsTheBody(t *testing.T) {
	_, rt := newRuntimeHarness(t)

	artifact, err := rt.OpenArtifact(context.Background(), runtime.ArtifactRef{
		RunID: "prompt-1", Filename: "out.png", Type: "output",
	})
	if err != nil {
		t.Fatalf("OpenArtifact: %v", err)
	}
	defer artifact.Body.Close()

	if artifact.ContentType != "image/png" {
		t.Errorf("ContentType = %q, want image/png", artifact.ContentType)
	}
	if artifact.Size != 6 {
		t.Errorf("Size = %d, want 6", artifact.Size)
	}
	body, err := io.ReadAll(artifact.Body)
	if err != nil {
		t.Fatalf("reading the artifact: %v", err)
	}
	if string(body) != "abcdef" {
		t.Errorf("body = %q, want %q", body, "abcdef")
	}
}

func TestNodeRuntimeAnswersReadOnlyCallsFromTheReportedInventory(t *testing.T) {
	// Probe, Health and Discover deliberately do not cross the tunnel: the
	// Agent probes its backend once and reports, and asking every replica to
	// re-probe every node would multiply that by the size of the fleet.
	h, rt := newRuntimeHarness(t)

	if got := rt.Descriptor(); got.ID != "ollama-1" || got.MaxConcurrent != 4 {
		t.Errorf("Descriptor() = %+v, want the reported ollama-1 descriptor", got)
	}
	probe, err := rt.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if probe.Version != "0.5.0" || !probe.IdentityVerified {
		t.Errorf("Probe() = %+v, want the reported evidence", probe)
	}
	health, err := rt.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.State != runtime.StateHealthy {
		t.Errorf("Health().State = %s, want healthy", health.State)
	}
	discovery, err := rt.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(discovery.Models) != 1 || discovery.Models[0].ID != "qwen3:8b" {
		t.Errorf("Discover().Models = %v, want [qwen3:8b]", discovery.Models)
	}

	// An instance the node never reported is not a connection failure; it is
	// a configuration one, and must not invite a retry elsewhere.
	unknown := h.srv.Runtime("mac-mini-01", "vllm-9")
	_, err = unknown.Health(context.Background())
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) {
		t.Fatalf("Health on an unknown instance = %v, want a *runtime.RuntimeError", err)
	}
	if !errors.Is(err, runtime.ErrRuntimeNotFound) {
		t.Errorf("error %v does not wrap ErrRuntimeNotFound", err)
	}
	if rtErr.Retryable {
		t.Error("Retryable = true for an instance that does not exist; retrying cannot help")
	}
}
