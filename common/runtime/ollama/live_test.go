package ollama_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/runtime/ollama"
)

// TestLiveOllama exercises the adapter against a real Ollama server, which
// the fake-server tests above cannot do: it is the only check that the
// endpoints, field names and capability strings this package assumes still
// match a shipping Ollama release.
//
// It is opt-in — set OLLAMA_BASE_URL (e.g. http://127.0.0.1:11434) to run
// it — so `go test ./...` stays hermetic on machines without Ollama.
func TestLiveOllama(t *testing.T) {
	baseURL := os.Getenv("OLLAMA_BASE_URL")
	if baseURL == "" {
		t.Skip("set OLLAMA_BASE_URL to run the live Ollama test")
	}

	cfg := runtime.Config{
		ID:      "live-ollama",
		Kind:    runtime.KindOllama,
		BaseURL: baseURL,
		// Local inference on a cold model is slow; these are generous
		// enough that a first-token wait is not mistaken for a failure.
		ProbeTimeout:      10 * time.Second,
		RequestTimeout:    5 * time.Minute,
		StreamIdleTimeout: 2 * time.Minute,
	}.Normalize()

	// A dedicated transport keeps this test's keep-alive connections out of
	// the shared default transport, so the package's goroutine-leak gate
	// sees them closed when the test ends.
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)

	rt, err := ollama.New(cfg, runtime.Dependencies{
		HTTPClient: &http.Client{Transport: transport},
		Clock:      runtime.NewSystemClock(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.Close()
	inference := rt.(*ollama.Runtime)

	ctx := context.Background()

	probe, err := inference.Probe(ctx)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !probe.IdentityVerified {
		t.Fatal("IdentityVerified = false against a real Ollama server")
	}
	t.Logf("probe: version=%s evidence=%s", probe.Version, probe.Evidence)

	health, err := inference.Health(ctx)
	if err != nil || health.State != runtime.StateHealthy {
		t.Fatalf("Health: state=%q err=%v", health.State, err)
	}
	t.Logf("health: state=%s latency=%s", health.State, health.Latency)

	discovery, err := inference.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, w := range discovery.Warnings {
		t.Logf("discovery warning: %s", w)
	}
	if len(discovery.Models) == 0 {
		t.Skip("the server has no models pulled; skipping the inference checks")
	}
	for _, m := range discovery.Models {
		t.Logf("model %s: chat=%s tools=%s vision=%s reasoning=%s embeddings=%s",
			m.ID,
			m.Capabilities.Resolve(runtime.CapabilityChat).Level,
			m.Capabilities.Resolve(runtime.CapabilityTools).Level,
			m.Capabilities.Resolve(runtime.CapabilityVision).Level,
			m.Capabilities.Resolve(runtime.CapabilityReasoning).Level,
			m.Capabilities.Resolve(runtime.CapabilityEmbeddings).Level,
		)
	}

	chatModelID := firstModelWith(discovery.Models, runtime.CapabilityChat)
	if chatModelID == "" {
		t.Skip("no chat-capable model available; skipping the inference checks")
	}

	maxTokens := 64
	req := runtime.ChatRequest{
		Model:     chatModelID,
		Messages:  []runtime.ChatMessage{{Role: "user", Content: "Reply with the single word: pong"}},
		MaxTokens: &maxTokens,
	}

	resp, err := inference.Chat(ctx, req)
	if err != nil {
		t.Fatalf("Chat against %s: %v", chatModelID, err)
	}
	t.Logf("chat: finish_reason=%q content=%q usage=%+v", resp.FinishReason, resp.Message.Content, resp.Usage)
	if resp.FinishReason == "" {
		t.Errorf("Chat returned no finish_reason")
	}

	stream, err := inference.ChatStream(ctx, req)
	if err != nil {
		t.Fatalf("ChatStream against %s: %v", chatModelID, err)
	}
	defer stream.Close()

	var (
		content strings.Builder
		events  int
	)
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv after %d events: %v", events, err)
		}
		events++
		content.WriteString(event.Delta.Content)
	}
	if events == 0 {
		t.Fatal("ChatStream delivered no events")
	}
	t.Logf("chat_stream: events=%d content=%q", events, content.String())
}

func firstModelWith(models []runtime.Model, capability runtime.Capability) string {
	for _, m := range models {
		if m.Capabilities.Resolve(capability).Level == runtime.SupportSupported {
			return m.ID
		}
	}
	return ""
}
