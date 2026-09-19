package httpapi_test

import (
	"errors"
	"fmt"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
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

// connectNode wires up nodeID with a Control stream reporting snap and one
// parked inference slot answered by handle.
func connectNode(t *testing.T, h *gatewaytest.Harness, nodeID, runtimeID string, snap runtime.Snapshot, handle gatewaytest.SlotHandler) {
	t.Helper()
	c := h.Connect(nodeID, runtimeID)
	c.Send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Status{Status: &tunnelv1.RuntimeStatus{
		Full:       true,
		ReportedAt: timestamppb.New(h.Clock.Now()),
		Snapshots:  tunnelwire.SnapshotsToProto([]runtime.Snapshot{snap}),
	}}})
	h.OpenSlot(nodeID, tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, nodeID+"-slot-1", handle)
	gatewaytest.WaitFor(t, "the slot to park on "+nodeID, func() bool { return gatewaytest.IdleCount(h, nodeID) == 1 })
	gatewaytest.WaitFor(t, "the inventory to arrive on "+nodeID, func() bool {
		info, _ := h.Srv.Node(nodeID)
		return len(info.Runtimes) == 1
	})
}

// connectNodeWithHandler is connectNode's variant that returns the
// AgentSlot, so a test can observe when the Agent's handler goroutine
// exits (e.g. after a client disconnect propagates through the tunnel).
func connectNodeWithHandler(t *testing.T, h *gatewaytest.Harness, nodeID, runtimeID string, snap runtime.Snapshot, handle gatewaytest.SlotHandler) *gatewaytest.AgentSlot {
	t.Helper()
	c := h.Connect(nodeID, runtimeID)
	c.Send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Status{Status: &tunnelv1.RuntimeStatus{
		Full:       true,
		ReportedAt: timestamppb.New(h.Clock.Now()),
		Snapshots:  tunnelwire.SnapshotsToProto([]runtime.Snapshot{snap}),
	}}})
	slot := h.OpenSlot(nodeID, tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, nodeID+"-slot-1", handle)
	gatewaytest.WaitFor(t, "the slot to park on "+nodeID, func() bool { return gatewaytest.IdleCount(h, nodeID) == 1 })
	gatewaytest.WaitFor(t, "the inventory to arrive on "+nodeID, func() bool {
		info, _ := h.Srv.Node(nodeID)
		return len(info.Runtimes) == 1
	})
	return slot
}

// chatHandler answers Chat with a canned, single response.
func chatHandler(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
	switch req.GetOperation() {
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
		return reply(gatewaytest.DataFrame(payload))
	case tunnelv1.Operation_OPERATION_CHAT_STREAM:
		for _, delta := range []string{"Hel", "lo"} {
			payload, err := tunnelwire.MarshalChatEvent(runtime.ChatEvent{
				ID:    "chat-1",
				Delta: runtime.ChatMessageDelta{Role: "assistant", Content: delta},
			})
			if err != nil {
				return err
			}
			if err := reply(gatewaytest.DataFrame(payload)); err != nil {
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
		return reply(gatewaytest.DataFrame(payload))
	}
	return errors.New("unsupported operation")
}

// chatHandlerEchoingParts answers Chat like chatHandler, but its echoed text
// also names every image_url, input_audio and file part it received
// (STATUS.md's P2 ChatMessage.Content structured rework and its follow-on
// multimodal input) — so a test can prove a part sent through a front door
// actually crossed the whole tunnel round trip (httpapi → runtime.ChatMessage
// → tunnelwire marshal → tunnel proto → tunnelwire unmarshal on this fake
// node) rather than merely asserting on a wire shape closer to the front
// door.
func chatHandlerEchoingParts(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
	if req.GetOperation() != tunnelv1.Operation_OPERATION_CHAT {
		return errors.New("unsupported operation")
	}
	in, err := tunnelwire.UnmarshalChatRequest(req.GetPayload())
	if err != nil {
		return err
	}
	msg := in.Messages[0]
	answer := "text=" + msg.Content
	for _, p := range msg.ContentParts {
		switch p.Type {
		case "text":
			answer += " text=" + p.Text
		case "image_url":
			if p.ImageURL != nil {
				answer += " image=" + p.ImageURL.URL
			}
		case "input_audio":
			if p.Audio != nil {
				answer += " audio=" + p.Audio.Data + "/" + p.Audio.Format
			}
		case "file":
			if p.File != nil {
				answer += " file=" + p.File.URL + "/" + p.File.Filename
			}
		}
	}
	payload, err := tunnelwire.MarshalChatResponse(runtime.ChatResponse{
		ID:           "chat-1",
		Model:        in.Model,
		Message:      runtime.ChatMessage{Role: "assistant", Content: answer},
		FinishReason: "stop",
		Usage:        runtime.Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8},
	})
	if err != nil {
		return err
	}
	return reply(gatewaytest.DataFrame(payload))
}

// blockingStreamHandler sends one event, signals started, then blocks on
// further replies. Because gatewaytest's Agent-to-server frame channel is
// unbuffered and the front door only calls Recv() once per SSE flush, this
// second reply() call does not return until the caller either keeps
// consuming the stream or tears it down — which is exactly the behavior a
// disconnect test needs to observe.
func blockingStreamHandler(started chan<- struct{}) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		for i := 0; i < 1000; i++ {
			payload, err := tunnelwire.MarshalChatEvent(runtime.ChatEvent{
				ID:    "chat-1",
				Delta: runtime.ChatMessageDelta{Role: "assistant", Content: fmt.Sprintf("chunk-%d", i)},
			})
			if err != nil {
				return err
			}
			if err := reply(gatewaytest.DataFrame(payload)); err != nil {
				return err
			}
			if i == 0 && started != nil {
				close(started)
				started = nil
			}
		}
		return nil
	}
}
