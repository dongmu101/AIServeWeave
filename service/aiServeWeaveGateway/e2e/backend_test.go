package e2e

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"AIServeWeave/common/runtime"
)

// backendKind is the Kind the scripted backend registers under. It has to be
// one of the four kinds runtime.Config.Validate knows, so the test registers
// its own factory under KindOllama in its own private registry rather than
// inventing a kind the config layer would reject. Nothing of Ollama's protocol
// is involved: the adapter for it has its own tests, and this package is about
// what happens between the Agent and the Gateway.
const backendKind = runtime.KindOllama

// backendConfig is what the scripted backend is told to do, carried through
// runtime.Config's Extra-free fields since a Factory receives only a Config.
// The values are process-global because a Factory has nowhere else to read
// them from; each test sets them before adding its instance.
type backendConfig struct {
	// firstTokenDelay is how long the backend waits before producing its
	// first streamed event. It is the backend's own share of TTFT, which the
	// latency test subtracts to isolate the tunnel's share.
	firstTokenDelay atomic.Int64
	// tokens is how many events a streamed response produces.
	tokens atomic.Int64
	// chatCalls counts non-streaming completions served.
	chatCalls atomic.Int64
	// streamCalls counts streamed completions served.
	streamCalls atomic.Int64
}

var backend backendConfig

// newBackend is the runtime.Factory for backendKind.
func newBackend(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
	return &scriptedRuntime{cfg: cfg, clock: deps.Clock}, nil
}

// scriptedRuntime is an inference backend with no network behind it. It is
// deliberately minimal: everything it answers is fixed, so a difference in
// what comes out of the far end of the tunnel is a tunnel difference.
type scriptedRuntime struct {
	cfg   runtime.Config
	clock runtime.Clock
}

var _ runtime.InferenceRuntime = (*scriptedRuntime)(nil)

func (r *scriptedRuntime) Descriptor() runtime.Descriptor {
	return runtime.Descriptor{
		ID:            r.cfg.ID,
		Kind:          r.cfg.Kind,
		BaseURL:       r.cfg.BaseURL,
		MaxConcurrent: r.cfg.MaxConcurrent,
	}
}

func (r *scriptedRuntime) Probe(context.Context) (runtime.ProbeResult, error) {
	return runtime.ProbeResult{
		Kind:             backendKind,
		Version:          "e2e",
		IdentityVerified: true,
		Evidence:         "scripted backend",
		ProbedAt:         time.Now(),
	}, nil
}

func (r *scriptedRuntime) Health(context.Context) (runtime.HealthReport, error) {
	return runtime.HealthReport{State: runtime.StateHealthy, CheckedAt: time.Now()}, nil
}

func (r *scriptedRuntime) Discover(context.Context) (runtime.Discovery, error) {
	caps := runtime.CapabilitySet{
		runtime.CapabilityChat: {
			Capability: runtime.CapabilityChat,
			Level:      runtime.SupportSupported,
			Source:     runtime.SourceEndpoint,
		},
		runtime.CapabilityChatStream: {
			Capability: runtime.CapabilityChatStream,
			Level:      runtime.SupportSupported,
			Source:     runtime.SourceEndpoint,
		},
	}
	return runtime.Discovery{
		Version:      "e2e",
		Models:       []runtime.Model{{ID: "e2e-model", Capabilities: caps}},
		Capabilities: caps,
		DiscoveredAt: time.Now(),
	}, nil
}

func (r *scriptedRuntime) Close() error { return nil }

func (r *scriptedRuntime) ListModels(context.Context) ([]runtime.Model, error) {
	return []runtime.Model{{ID: "e2e-model"}}, nil
}

func (r *scriptedRuntime) Chat(ctx context.Context, req runtime.ChatRequest) (runtime.ChatResponse, error) {
	backend.chatCalls.Add(1)
	prompt := ""
	if len(req.Messages) > 0 {
		prompt = req.Messages[len(req.Messages)-1].Content
	}
	return runtime.ChatResponse{
		ID:           "e2e-1",
		Model:        req.Model,
		Message:      runtime.ChatMessage{Role: "assistant", Content: "echo:" + prompt},
		FinishReason: "stop",
		Usage:        runtime.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
		CreatedAt:    time.Now(),
	}, nil
}

func (r *scriptedRuntime) ChatStream(ctx context.Context, req runtime.ChatRequest) (runtime.Stream[runtime.ChatEvent], error) {
	backend.streamCalls.Add(1)
	stream := runtime.NewChanStream[runtime.ChatEvent](0)
	tokens := int(backend.tokens.Load())
	if tokens == 0 {
		tokens = 4
	}
	delay := time.Duration(backend.firstTokenDelay.Load())

	go func() {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				stream.CloseWithError(ctx.Err())
				return
			}
		}
		for i := range tokens {
			ev := runtime.ChatEvent{
				ID:    "e2e-1",
				Model: req.Model,
				Delta: runtime.ChatMessageDelta{Role: "assistant", Content: fmt.Sprintf("t%d", i)},
			}
			if !stream.Send(ev) {
				return
			}
		}
		stream.CloseWithError(nil)
	}()
	return stream, nil
}

func (r *scriptedRuntime) Embed(ctx context.Context, req runtime.EmbeddingRequest) (runtime.EmbeddingResponse, error) {
	return runtime.EmbeddingResponse{
		Model: req.Model,
		Data:  []runtime.Embedding{{Index: 0, Vector: []float32{1, 0}}},
	}, nil
}
