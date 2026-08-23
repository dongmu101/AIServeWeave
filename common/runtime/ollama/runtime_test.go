package ollama_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/runtime/internal/runtimetest"
	"AIServeWeave/service/aiServeWeaveAgent/runtime/ollama"
)

// --- fake Ollama server -----------------------------------------------

type fakeModel struct {
	Name   string
	Digest string
	// Capabilities is inlined into /api/tags when non-nil, mirroring newer
	// Ollama servers; a nil value forces the /api/show fallback path.
	Capabilities []string
}

type fakeOllama struct {
	server *httptest.Server

	mu             sync.Mutex
	version        string
	models         []fakeModel
	showCaps       map[string][]string
	showCalls      int
	showFails      bool
	nativeDisabled bool
	versionStatus  int
	chatGate       chan struct{}
	chatEntered    chan struct{}
}

func newFakeOllama(t *testing.T) *fakeOllama {
	t.Helper()
	f := &fakeOllama{
		version:  "0.32.14",
		showCaps: map[string][]string{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.nativeDisabled {
			writeJSONError(w, http.StatusNotFound, "404 page not found")
			return
		}
		if f.versionStatus != 0 {
			writeJSONError(w, f.versionStatus, "server error")
			return
		}
		writeJSON(w, map[string]string{"version": f.version})
	})
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.nativeDisabled {
			writeJSONError(w, http.StatusNotFound, "404 page not found")
			return
		}
		models := make([]map[string]any, 0, len(f.models))
		for _, m := range f.models {
			entry := map[string]any{"name": m.Name, "model": m.Name, "digest": m.Digest}
			if m.Capabilities != nil {
				entry["capabilities"] = m.Capabilities
			}
			models = append(models, entry)
		}
		writeJSON(w, map[string]any{"models": models})
	})
	mux.HandleFunc("/api/show", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		f.mu.Lock()
		defer f.mu.Unlock()
		f.showCalls++
		if f.showFails {
			writeJSONError(w, http.StatusInternalServerError, "show failed")
			return
		}
		writeJSON(w, map[string]any{"capabilities": f.showCaps[req.Model]})
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		data := make([]map[string]any, 0, len(f.models))
		for _, m := range f.models {
			data = append(data, map[string]any{"id": m.Name, "object": "model"})
		}
		writeJSON(w, map[string]any{"object": "list", "data": data})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		gate, entered := f.chatGate, f.chatEntered
		f.chatEntered = nil // signal only the first arrival
		f.mu.Unlock()
		if entered != nil {
			close(entered)
		}
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}

		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if stream, _ := req["stream"].(bool); stream {
			writeChatSSE(w)
			return
		}
		writeJSON(w, map[string]any{
			"id":      "chatcmpl-1",
			"model":   req["model"],
			"created": 1,
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "pong"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	})
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		writeJSON(w, map[string]any{
			"model": req["model"],
			"data":  []map[string]any{{"index": 0, "embedding": []float32{0.5, 0.25}}},
		})
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message})
}

func writeChatSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	chunks := []string{
		`{"id":"chatcmpl-1","model":"chat-model","choices":[{"delta":{"role":"assistant","content":"po"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-1","model":"chat-model","choices":[{"delta":{"content":"ng"},"finish_reason":"stop"}]}`,
		"[DONE]",
	}
	for _, chunk := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func (f *fakeOllama) setModels(models ...fakeModel) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.models = models
}

func (f *fakeOllama) countShowCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.showCalls
}

// --- helpers ----------------------------------------------------------

func newRuntime(t *testing.T, f *fakeOllama, mutate ...func(*runtime.Config)) *ollama.Runtime {
	t.Helper()
	cfg := runtime.Config{
		ID:      "test-ollama",
		Kind:    runtime.KindOllama,
		BaseURL: f.server.URL,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	deps := runtime.Dependencies{
		HTTPClient: f.server.Client(),
		Clock:      runtimetest.NewClock(time.Unix(1700000000, 0)),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	rt, err := ollama.New(cfg.Normalize(), deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	inference, ok := rt.(*ollama.Runtime)
	if !ok {
		t.Fatalf("New returned %T, want *ollama.Runtime", rt)
	}
	return inference
}

func requireErrorCode(t *testing.T, err error, want runtime.ErrorCode) *runtime.RuntimeError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %q, got nil", want)
	}
	var rerr *runtime.RuntimeError
	if !errors.As(err, &rerr) {
		t.Fatalf("expected *runtime.RuntimeError, got %T: %v", err, err)
	}
	if rerr.Code != want {
		t.Fatalf("error code = %q, want %q (err: %v)", rerr.Code, want, err)
	}
	return rerr
}

const (
	chatModel  = "chat-model"
	embedModel = "embed-model"
	toolModel  = "tool-model"
)

func standardModels() []fakeModel {
	return []fakeModel{
		{Name: chatModel, Digest: "sha256:chat", Capabilities: []string{"completion", "thinking"}},
		{Name: embedModel, Digest: "sha256:embed", Capabilities: []string{"embedding"}},
		{Name: toolModel, Digest: "sha256:tool", Capabilities: []string{"completion", "tools", "vision"}},
	}
}

// --- probe ------------------------------------------------------------

func TestProbeVerifiesIdentityFromNativeEndpoints(t *testing.T) {
	f := newFakeOllama(t)
	f.setModels(standardModels()...)
	rt := newRuntime(t, f)

	result, err := rt.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if result.Kind != runtime.KindOllama {
		t.Errorf("Kind = %q, want %q", result.Kind, runtime.KindOllama)
	}
	if !result.IdentityVerified {
		t.Error("IdentityVerified = false, want true")
	}
	if result.Version != "0.32.14" {
		t.Errorf("Version = %q, want %q", result.Version, "0.32.14")
	}
	if !strings.Contains(result.Evidence, "/api/version") || !strings.Contains(result.Evidence, "3 model(s)") {
		t.Errorf("Evidence = %q, want it to cite both native endpoints", result.Evidence)
	}
}

func TestProbeRejectsOpenAICompatibleBackendWithoutNativeAPI(t *testing.T) {
	f := newFakeOllama(t)
	f.setModels(standardModels()...)
	f.mu.Lock()
	f.nativeDisabled = true // only /v1/* answers, as vLLM or SGLang would
	f.mu.Unlock()
	rt := newRuntime(t, f)

	_, err := rt.Probe(context.Background())
	rerr := requireErrorCode(t, err, runtime.ErrorProbeMismatch)
	if !strings.Contains(rerr.Message, "/api/version") {
		t.Errorf("Message = %q, want it to name the endpoint that failed", rerr.Message)
	}
}

func TestProbeKeepsServerErrorsDistinctFromMismatch(t *testing.T) {
	f := newFakeOllama(t)
	f.mu.Lock()
	f.versionStatus = http.StatusInternalServerError
	f.mu.Unlock()
	rt := newRuntime(t, f)

	// A 5xx means an Ollama server that is unwell, not a different backend;
	// misreporting it as a mismatch would make Manager drop the instance
	// permanently instead of retrying it.
	_, err := rt.Probe(context.Background())
	requireErrorCode(t, err, runtime.ErrorUpstream)
}

// --- health -----------------------------------------------------------

func TestHealthReportsHealthyWithoutRunningInference(t *testing.T) {
	f := newFakeOllama(t)
	rt := newRuntime(t, f)

	report, err := rt.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if report.State != runtime.StateHealthy {
		t.Errorf("State = %q, want %q", report.State, runtime.StateHealthy)
	}
	if report.CheckedAt.IsZero() {
		t.Error("CheckedAt is zero, want the clock's time")
	}
}

func TestHealthReportsUnhealthyWithSanitizedSummary(t *testing.T) {
	f := newFakeOllama(t)
	f.mu.Lock()
	f.versionStatus = http.StatusServiceUnavailable
	f.mu.Unlock()
	rt := newRuntime(t, f, func(c *runtime.Config) { c.APIKey = "super-secret" })

	report, err := rt.Health(context.Background())
	if err == nil {
		t.Fatal("Health: expected an error")
	}
	if report.State != runtime.StateUnhealthy {
		t.Errorf("State = %q, want %q", report.State, runtime.StateUnhealthy)
	}
	if report.ErrorSummary == "" {
		t.Error("ErrorSummary is empty, want a diagnostic message")
	}
	if strings.Contains(report.ErrorSummary, "super-secret") {
		t.Errorf("ErrorSummary leaked the API key: %q", report.ErrorSummary)
	}
}

// --- discover ---------------------------------------------------------

func TestDiscoverUsesInlineTagCapabilities(t *testing.T) {
	f := newFakeOllama(t)
	f.setModels(standardModels()...)
	rt := newRuntime(t, f)

	discovery, err := rt.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got := f.countShowCalls(); got != 0 {
		t.Errorf("/api/show calls = %d, want 0 when /api/tags already carries capabilities", got)
	}
	if len(discovery.Models) != 3 {
		t.Fatalf("len(Models) = %d, want 3", len(discovery.Models))
	}

	byID := map[string]runtime.CapabilitySet{}
	for _, m := range discovery.Models {
		byID[m.ID] = m.Capabilities
	}
	if lvl := byID[chatModel].Resolve(runtime.CapabilityChat).Level; lvl != runtime.SupportSupported {
		t.Errorf("%s chat = %q, want supported", chatModel, lvl)
	}
	if lvl := byID[chatModel].Resolve(runtime.CapabilityEmbeddings).Level; lvl != runtime.SupportUnsupported {
		t.Errorf("%s embeddings = %q, want unsupported", chatModel, lvl)
	}
	if lvl := byID[chatModel].Resolve(runtime.CapabilityReasoning).Level; lvl != runtime.SupportSupported {
		t.Errorf("%s reasoning = %q, want supported", chatModel, lvl)
	}
	if lvl := byID[embedModel].Resolve(runtime.CapabilityEmbeddings).Level; lvl != runtime.SupportSupported {
		t.Errorf("%s embeddings = %q, want supported", embedModel, lvl)
	}
	if lvl := byID[toolModel].Resolve(runtime.CapabilityVision).Level; lvl != runtime.SupportSupported {
		t.Errorf("%s vision = %q, want supported", toolModel, lvl)
	}
	if lvl := discovery.Capabilities.Resolve(runtime.CapabilityChat).Level; lvl != runtime.SupportSupported {
		t.Errorf("runtime chat = %q, want supported", lvl)
	}
	if lvl := discovery.Capabilities.Resolve(runtime.CapabilityParallelToolCalls).Level; lvl != runtime.SupportUnknown {
		t.Errorf("runtime parallel_tool_calls = %q, want unknown", lvl)
	}
}

func TestDiscoverFallsBackToShowAndCachesByDigest(t *testing.T) {
	f := newFakeOllama(t)
	f.setModels(fakeModel{Name: embedModel, Digest: "sha256:embed"}) // no inline capabilities
	f.mu.Lock()
	f.showCaps[embedModel] = []string{"embedding"}
	f.mu.Unlock()
	rt := newRuntime(t, f)

	discovery, err := rt.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got := f.countShowCalls(); got != 1 {
		t.Fatalf("/api/show calls = %d, want 1", got)
	}
	if lvl := discovery.Models[0].Capabilities.Resolve(runtime.CapabilityEmbeddings).Level; lvl != runtime.SupportSupported {
		t.Errorf("embeddings = %q, want supported", lvl)
	}

	if _, err := rt.Discover(context.Background()); err != nil {
		t.Fatalf("second Discover: %v", err)
	}
	if got := f.countShowCalls(); got != 1 {
		t.Errorf("/api/show calls = %d after a second Discover, want the digest cache to keep it at 1", got)
	}
}

func TestDiscoverWarnsInsteadOfFailingWhenShowFails(t *testing.T) {
	f := newFakeOllama(t)
	f.setModels(fakeModel{Name: chatModel, Digest: "sha256:chat"})
	f.mu.Lock()
	f.showFails = true
	f.mu.Unlock()
	rt := newRuntime(t, f)

	discovery, err := rt.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(discovery.Warnings) != 1 || !strings.Contains(discovery.Warnings[0], chatModel) {
		t.Fatalf("Warnings = %v, want one warning naming %q", discovery.Warnings, chatModel)
	}
	if lvl := discovery.Models[0].Capabilities.Resolve(runtime.CapabilityVision).Level; lvl != runtime.SupportUnknown {
		t.Errorf("vision = %q, want unknown when /api/show failed", lvl)
	}
}

func TestDiscoverLeavesAdvancedCapabilitiesUnknownOnOlderServers(t *testing.T) {
	f := newFakeOllama(t)
	f.setModels(standardModels()...)
	f.mu.Lock()
	f.version = "0.1.30"
	f.mu.Unlock()
	rt := newRuntime(t, f)

	discovery, err := rt.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if lvl := discovery.Capabilities.Resolve(runtime.CapabilityChat).Level; lvl != runtime.SupportSupported {
		t.Errorf("chat = %q, want supported", lvl)
	}
	if lvl := discovery.Capabilities.Resolve(runtime.CapabilityStructuredOutput).Level; lvl != runtime.SupportUnknown {
		t.Errorf("structured_output = %q, want unknown below the profile floor", lvl)
	}
}

func TestDiscoverAppliesConfigOverrides(t *testing.T) {
	f := newFakeOllama(t)
	f.setModels(standardModels()...)
	rt := newRuntime(t, f, func(c *runtime.Config) {
		c.CapabilityOverrides = map[runtime.Capability]runtime.SupportLevel{
			runtime.CapabilityParallelToolCalls: runtime.SupportSupported,
		}
	})

	discovery, err := rt.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	ev := discovery.Capabilities.Resolve(runtime.CapabilityParallelToolCalls)
	if ev.Level != runtime.SupportSupported {
		t.Errorf("parallel_tool_calls = %q, want supported", ev.Level)
	}
	if ev.Source != runtime.SourceConfigOverride {
		t.Errorf("Source = %q, want %q so operators can tell asserted from observed", ev.Source, runtime.SourceConfigOverride)
	}
}

func TestListModelsReportsDiscoveredModels(t *testing.T) {
	f := newFakeOllama(t)
	f.setModels(standardModels()...)
	rt := newRuntime(t, f)

	models, err := rt.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("len(models) = %d, want 3", len(models))
	}
}

// --- capability gating ------------------------------------------------

func TestChatBeforeDiscoverIsRejectedAsUnknown(t *testing.T) {
	f := newFakeOllama(t)
	f.setModels(standardModels()...)
	rt := newRuntime(t, f)

	_, err := rt.Chat(context.Background(), runtime.ChatRequest{
		Model:    chatModel,
		Messages: []runtime.ChatMessage{{Role: "user", Content: "ping"}},
	})
	requireErrorCode(t, err, runtime.ErrorCapability)
	if !errors.Is(err, runtime.ErrCapabilityUnknown) {
		t.Errorf("expected ErrCapabilityUnknown, got %v", err)
	}
}

func TestChatSucceedsAfterDiscover(t *testing.T) {
	f := newFakeOllama(t)
	f.setModels(standardModels()...)
	rt := newRuntime(t, f)
	mustDiscover(t, rt)

	resp, err := rt.Chat(context.Background(), runtime.ChatRequest{
		Model:    chatModel,
		Messages: []runtime.ChatMessage{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Message.Content != "pong" {
		t.Errorf("Content = %q, want %q", resp.Message.Content, "pong")
	}
	if resp.Usage.TotalTokens != 2 {
		t.Errorf("TotalTokens = %d, want 2", resp.Usage.TotalTokens)
	}
}

func TestEmbedIsRejectedForNonEmbeddingModel(t *testing.T) {
	f := newFakeOllama(t)
	f.setModels(standardModels()...)
	rt := newRuntime(t, f)
	mustDiscover(t, rt)

	_, err := rt.Embed(context.Background(), runtime.EmbeddingRequest{
		Model: chatModel,
		Input: []string{"hello"},
	})
	requireErrorCode(t, err, runtime.ErrorCapability)
	if !errors.Is(err, runtime.ErrCapabilityUnsupported) {
		t.Errorf("expected ErrCapabilityUnsupported, got %v", err)
	}
}

func TestEmbedSucceedsForEmbeddingModel(t *testing.T) {
	f := newFakeOllama(t)
	f.setModels(standardModels()...)
	rt := newRuntime(t, f)
	mustDiscover(t, rt)

	resp, err := rt.Embed(context.Background(), runtime.EmbeddingRequest{
		Model: embedModel,
		Input: []string{"hello"},
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(resp.Data) != 1 || len(resp.Data[0].Vector) != 2 {
		t.Fatalf("Data = %+v, want one 2-dimensional vector", resp.Data)
	}
}

func TestChatWithToolsIsRejectedForModelWithoutToolSupport(t *testing.T) {
	f := newFakeOllama(t)
	f.setModels(standardModels()...)
	rt := newRuntime(t, f)
	mustDiscover(t, rt)

	_, err := rt.Chat(context.Background(), runtime.ChatRequest{
		Model:    chatModel,
		Messages: []runtime.ChatMessage{{Role: "user", Content: "ping"}},
		Tools:    []runtime.Tool{{Type: "function", Function: runtime.FunctionDefinition{Name: "now"}}},
	})
	requireErrorCode(t, err, runtime.ErrorCapability)
}

func TestChatWithToolsSucceedsForToolCapableModel(t *testing.T) {
	f := newFakeOllama(t)
	f.setModels(standardModels()...)
	rt := newRuntime(t, f)
	mustDiscover(t, rt)

	if _, err := rt.Chat(context.Background(), runtime.ChatRequest{
		Model:    toolModel,
		Messages: []runtime.ChatMessage{{Role: "user", Content: "ping"}},
		Tools:    []runtime.Tool{{Type: "function", Function: runtime.FunctionDefinition{Name: "now"}}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
}

// --- streaming --------------------------------------------------------

func TestChatStreamDeliversDeltasAndReleasesTheSlotOnClose(t *testing.T) {
	f := newFakeOllama(t)
	f.setModels(standardModels()...)
	rt := newRuntime(t, f, func(c *runtime.Config) { c.MaxConcurrent = 1 })
	mustDiscover(t, rt)

	stream, err := rt.ChatStream(context.Background(), runtime.ChatRequest{
		Model:    chatModel,
		Messages: []runtime.ChatMessage{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var content strings.Builder
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		content.WriteString(event.Delta.Content)
	}
	if content.String() != "pong" {
		t.Errorf("streamed content = %q, want %q", content.String(), "pong")
	}
	if !stream.Committed() {
		t.Error("Committed = false after delivering events, want true")
	}

	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The slot the stream held must be back, or this single-slot instance
	// would be wedged for the rest of its life.
	if _, err := rt.Chat(context.Background(), runtime.ChatRequest{
		Model:    chatModel,
		Messages: []runtime.ChatMessage{{Role: "user", Content: "ping"}},
	}); err != nil {
		t.Fatalf("Chat after stream close: %v", err)
	}
}

func TestChatStreamStopsWhenTheCallerCancels(t *testing.T) {
	f := newFakeOllama(t)
	f.setModels(standardModels()...)
	rt := newRuntime(t, f)
	mustDiscover(t, rt)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := rt.ChatStream(ctx, runtime.ChatRequest{
		Model:    chatModel,
		Messages: []runtime.ChatMessage{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	cancel()

	// After cancellation Recv must terminate rather than block: either with
	// the decoder's timeout error or with the stream's normal end, but never
	// by hanging.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, err := stream.Recv(); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Recv did not return after the caller cancelled the context")
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// --- concurrency and lifecycle ----------------------------------------

func TestConcurrencyLimitIsReportedAsBackpressure(t *testing.T) {
	f := newFakeOllama(t)
	f.setModels(standardModels()...)
	gate := make(chan struct{})
	entered := make(chan struct{})
	f.mu.Lock()
	f.chatGate, f.chatEntered = gate, entered
	f.mu.Unlock()

	rt := newRuntime(t, f, func(c *runtime.Config) { c.MaxConcurrent = 1 })
	mustDiscover(t, rt)

	firstDone := make(chan error, 1)
	go func() {
		_, err := rt.Chat(context.Background(), runtime.ChatRequest{
			Model:    chatModel,
			Messages: []runtime.ChatMessage{{Role: "user", Content: "ping"}},
		})
		firstDone <- err
	}()

	// Waiting for the server to report the first request as in flight is
	// what makes this deterministic: by then the slot is provably held, so
	// the second call needs no retry loop or sleep.
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first Chat never reached the server")
	}

	_, err := rt.Chat(context.Background(), runtime.ChatRequest{
		Model:    chatModel,
		Messages: []runtime.ChatMessage{{Role: "user", Content: "ping"}},
	})
	rerr := requireErrorCode(t, err, runtime.ErrorBackpressure)
	if !errors.Is(rerr, runtime.ErrConcurrencyLimit) {
		t.Errorf("expected ErrConcurrencyLimit, got %v", err)
	}

	close(gate)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Chat: %v", err)
	}
}

func TestCloseRejectsSubsequentCallsAndIsIdempotent(t *testing.T) {
	f := newFakeOllama(t)
	f.setModels(standardModels()...)
	rt := newRuntime(t, f)
	mustDiscover(t, rt)

	if err := rt.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	_, err := rt.Chat(context.Background(), runtime.ChatRequest{
		Model:    chatModel,
		Messages: []runtime.ChatMessage{{Role: "user", Content: "ping"}},
	})
	requireErrorCode(t, err, runtime.ErrorClosed)

	if _, err := rt.Probe(context.Background()); err == nil {
		t.Error("Probe after Close: expected an error")
	}
	if _, err := rt.Discover(context.Background()); err == nil {
		t.Error("Discover after Close: expected an error")
	}
}

func TestDescriptorReportsSchedulingIdentity(t *testing.T) {
	f := newFakeOllama(t)
	rt := newRuntime(t, f, func(c *runtime.Config) {
		c.MaxConcurrent = 7
		c.Exclusive = true
		c.APIKey = "super-secret"
	})

	d := rt.Descriptor()
	if d.ID != "test-ollama" || d.Kind != runtime.KindOllama {
		t.Errorf("Descriptor identity = %+v", d)
	}
	if d.MaxConcurrent != 7 || !d.Exclusive {
		t.Errorf("Descriptor scheduling = %+v", d)
	}
}

func TestNewRejectsAnotherKind(t *testing.T) {
	f := newFakeOllama(t)
	_, err := ollama.New(runtime.Config{
		ID:      "wrong-kind",
		Kind:    runtime.KindVLLM,
		BaseURL: f.server.URL,
	}.Normalize(), runtime.Dependencies{HTTPClient: f.server.Client()})
	requireErrorCode(t, err, runtime.ErrorInvalidConfig)
}

func TestNewIsUsableAsARegistryFactory(t *testing.T) {
	f := newFakeOllama(t)
	registry := runtime.NewRegistry()
	if err := registry.Register(runtime.KindOllama, ollama.New); err != nil {
		t.Fatalf("Register: %v", err)
	}
	rt, err := registry.Create(runtime.Config{
		ID:      "registered-ollama",
		Kind:    runtime.KindOllama,
		BaseURL: f.server.URL,
	}, runtime.Dependencies{
		HTTPClient: f.server.Client(),
		Clock:      runtimetest.NewClock(time.Unix(1700000000, 0)),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:    stubMetrics{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer rt.Close()
	if _, ok := rt.(runtime.InferenceRuntime); !ok {
		t.Fatalf("registry produced %T, which is not an InferenceRuntime", rt)
	}
}

func mustDiscover(t *testing.T, rt *ollama.Runtime) {
	t.Helper()
	if _, err := rt.Discover(context.Background()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
}

// stubMetrics satisfies runtime.Dependencies' Metrics requirement for the
// Registry path without recording anything.
type stubMetrics struct{}

func (stubMetrics) Counter(string, map[string]string) runtime.Counter     { return stubInstrument{} }
func (stubMetrics) Gauge(string, map[string]string) runtime.Gauge         { return stubInstrument{} }
func (stubMetrics) Histogram(string, map[string]string) runtime.Histogram { return stubInstrument{} }

type stubInstrument struct{}

func (stubInstrument) Add(float64)     {}
func (stubInstrument) Set(float64)     {}
func (stubInstrument) Observe(float64) {}
