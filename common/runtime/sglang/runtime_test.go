package sglang_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/internal/runtimetest"
	"AIServeWeave/common/runtime/sglang"
)

const servedModel = "Qwen/Qwen2.5-7B-Instruct"

const (
	healthEndpoint     = "/health"
	modelsEndpoint     = "/v1/models"
	serverInfoEndpoint = "/get_server_info"
	generateEndpoint   = "/generate"
)

// --- fake SGLang server -----------------------------------------------

type fakeSGLang struct {
	server *httptest.Server

	mu              sync.Mutex
	models          []string
	healthStatus    int
	healthAbsent    bool
	serverInfo      any // nil means "route absent"
	serverInfoRaw   string
	serverInfoError int
	chatStatus      int
	chatError       string
	paths           []string
}

func newFakeSGLang(t *testing.T, opts ...func(*fakeSGLang)) *fakeSGLang {
	t.Helper()
	f := &fakeSGLang{models: []string{servedModel}}
	for _, opt := range opts {
		opt(f)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		writeJSONError(w, http.StatusNotFound, "Not Found")
	})
	mux.HandleFunc(healthEndpoint, func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		f.mu.Lock()
		absent, status := f.healthAbsent, f.healthStatus
		f.mu.Unlock()
		switch {
		case absent:
			writeJSONError(w, http.StatusNotFound, "Not Found")
		case status != 0:
			writeJSONError(w, status, "engine is dead")
		default:
			w.WriteHeader(http.StatusOK) // empty body, as SGLang answers
		}
	})
	mux.HandleFunc(modelsEndpoint, func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		f.mu.Lock()
		defer f.mu.Unlock()
		data := make([]map[string]any, 0, len(f.models))
		for _, id := range f.models {
			data = append(data, map[string]any{"id": id, "object": "model"})
		}
		writeJSON(w, map[string]any{"object": "list", "data": data})
	})
	mux.HandleFunc(serverInfoEndpoint, func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		f.mu.Lock()
		info, raw, status := f.serverInfo, f.serverInfoRaw, f.serverInfoError
		f.mu.Unlock()
		switch {
		case status != 0:
			writeJSONError(w, status, "not available")
		case raw != "":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, raw)
		case info != nil:
			writeJSON(w, info)
		default:
			writeJSONError(w, http.StatusNotFound, "Not Found")
		}
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		f.mu.Lock()
		status, message := f.chatStatus, f.chatError
		f.mu.Unlock()
		if status != 0 {
			writeJSONError(w, status, message)
			return
		}

		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if stream, _ := req["stream"].(bool); stream {
			writeChatSSE(w)
			return
		}
		writeJSON(w, map[string]any{
			"id":      "cmpl-1",
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
		f.record(r)
		writeJSON(w, map[string]any{
			"model": servedModel,
			"data":  []map[string]any{{"index": 0, "embedding": []float32{0.5, 0.25}}},
		})
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeSGLang) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.Path)
}

func (f *fakeSGLang) requestedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	unique := map[string]bool{}
	for _, p := range f.paths {
		unique[p] = true
	}
	out := make([]string, 0, len(unique))
	for p := range unique {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (f *fakeSGLang) countPath(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.paths {
		if p == path {
			n++
		}
	}
	return n
}

func (f *fakeSGLang) setHealthAbsent(absent bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.healthAbsent = absent
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": message, "type": "invalid_request_error"}})
}

func writeChatSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, chunk := range []string{
		`{"id":"cmpl-1","choices":[{"delta":{"role":"assistant","content":"po"},"finish_reason":null}]}`,
		`{"id":"cmpl-1","choices":[{"delta":{"content":"ng"},"finish_reason":"stop"}]}`,
		"[DONE]",
	} {
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// --- helpers ----------------------------------------------------------

func newRuntime(t *testing.T, f *fakeSGLang, mutate ...func(*runtime.Config)) *sglang.Runtime {
	t.Helper()
	cfg := runtime.Config{
		ID:      "test-sglang",
		Kind:    runtime.KindSGLang,
		BaseURL: f.server.URL,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	rt, err := sglang.New(cfg.Normalize(), runtime.Dependencies{
		HTTPClient: f.server.Client(),
		Clock:      runtimetest.NewClock(time.Unix(1700000000, 0)),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	adapter, ok := rt.(*sglang.Runtime)
	if !ok {
		t.Fatalf("New returned %T, want *sglang.Runtime", rt)
	}
	return adapter
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

func mustProbe(t *testing.T, rt *sglang.Runtime) runtime.ProbeResult {
	t.Helper()
	result, err := rt.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	return result
}

func mustDiscover(t *testing.T, rt *sglang.Runtime) runtime.Discovery {
	t.Helper()
	discovery, err := rt.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	return discovery
}

func mustHealth(t *testing.T, rt *sglang.Runtime) runtime.HealthReport {
	t.Helper()
	report, err := rt.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	return report
}

func chatRequest() runtime.ChatRequest {
	return runtime.ChatRequest{
		Model:    servedModel,
		Messages: []runtime.ChatMessage{{Role: "user", Content: "ping"}},
	}
}

func containsSubstring(values []string, substr string) bool {
	for _, v := range values {
		if strings.Contains(v, substr) {
			return true
		}
	}
	return false
}

// --- probe ------------------------------------------------------------

func TestProbeNeverClaimsVerifiedIdentity(t *testing.T) {
	f := newFakeSGLang(t)
	rt := newRuntime(t, f)

	result := mustProbe(t, rt)
	if result.Kind != runtime.KindSGLang {
		t.Errorf("Kind = %q, want %q", result.Kind, runtime.KindSGLang)
	}
	// An OpenAI-compatible response is identical to vLLM's; claiming
	// verification here would let a mis-typed config look confirmed.
	if result.IdentityVerified {
		t.Error("IdentityVerified = true, want false for a backend with no identifying endpoint")
	}
	if !strings.Contains(result.Evidence, "asserted by configuration") {
		t.Errorf("Evidence = %q, want it to state the kind came from configuration", result.Evidence)
	}
	if !strings.Contains(result.Evidence, "1 model(s)") {
		t.Errorf("Evidence = %q, want the model count", result.Evidence)
	}
}

func TestProbeFailsWhenModelsEndpointIsAbsent(t *testing.T) {
	f := newFakeSGLang(t)
	rt := newRuntime(t, f, func(c *runtime.Config) { c.BaseURL = f.server.URL + "/not-a-server" })

	_, err := rt.Probe(context.Background())
	requireErrorCode(t, err, runtime.ErrorProbeMismatch)
}

func TestProbeRecordsThatHealthIsPresent(t *testing.T) {
	f := newFakeSGLang(t)
	rt := newRuntime(t, f)

	result := mustProbe(t, rt)
	if !strings.Contains(result.Evidence, healthEndpoint+" is present") {
		t.Errorf("Evidence = %q, want it to record that /health exists", result.Evidence)
	}
	if discovery := mustDiscover(t, rt); containsSubstring(discovery.Warnings, "fall back") {
		t.Errorf("Warnings = %v, want no degradation note when /health is present", discovery.Warnings)
	}
}

// --- health degradation -----------------------------------------------

func TestHealthDegradesWhenHealthEndpointIsAbsent(t *testing.T) {
	f := newFakeSGLang(t, func(f *fakeSGLang) { f.healthAbsent = true })
	rt := newRuntime(t, f)

	result := mustProbe(t, rt)
	if !strings.Contains(result.Evidence, "is absent") {
		t.Errorf("Evidence = %q, want it to record the missing /health route", result.Evidence)
	}

	report := mustHealth(t, rt)
	if report.State != runtime.StateHealthy {
		t.Errorf("State = %q, want %q via the degraded path", report.State, runtime.StateHealthy)
	}

	// Manager mirrors Discovery.Warnings into Snapshot.Degraded, so this is
	// what makes the degradation visible to operators.
	discovery := mustDiscover(t, rt)
	if !containsSubstring(discovery.Warnings, healthEndpoint) {
		t.Errorf("Warnings = %v, want a note naming the missing endpoint", discovery.Warnings)
	}
	if !containsSubstring(discovery.Warnings, "not that the inference engine is") {
		t.Errorf("Warnings = %v, want the note to state what the fallback does not prove", discovery.Warnings)
	}
}

func TestHealthDegradesWhenTheRouteDisappearsLater(t *testing.T) {
	f := newFakeSGLang(t)
	rt := newRuntime(t, f)
	mustProbe(t, rt)
	mustHealth(t, rt) // /health still present here

	// A server can be replaced by a build without the route; the next check
	// must degrade on its own rather than reporting the instance dead.
	f.setHealthAbsent(true)
	report := mustHealth(t, rt)
	if report.State != runtime.StateHealthy {
		t.Errorf("State = %q, want the check to degrade rather than fail", report.State)
	}
	if discovery := mustDiscover(t, rt); !containsSubstring(discovery.Warnings, healthEndpoint) {
		t.Errorf("Warnings = %v, want the degradation recorded", discovery.Warnings)
	}
}

func TestHealthStopsCallingTheAbsentRouteOnceDegraded(t *testing.T) {
	f := newFakeSGLang(t, func(f *fakeSGLang) { f.healthAbsent = true })
	rt := newRuntime(t, f)
	mustProbe(t, rt)

	before := f.countPath(healthEndpoint)
	for i := 0; i < 3; i++ {
		mustHealth(t, rt)
	}
	if got := f.countPath(healthEndpoint) - before; got != 0 {
		t.Errorf("%s was called %d more times after degrading, want 0", healthEndpoint, got)
	}
	if f.countPath(modelsEndpoint) == 0 {
		t.Errorf("the degraded health path never called %s", modelsEndpoint)
	}
}

func TestHealthFailureIsNotTreatedAsDegradation(t *testing.T) {
	f := newFakeSGLang(t, func(f *fakeSGLang) { f.healthStatus = http.StatusServiceUnavailable })
	rt := newRuntime(t, f, func(c *runtime.Config) { c.APIKey = "super-secret" })

	report, err := rt.Health(context.Background())
	if err == nil {
		t.Fatal("Health: expected an error")
	}
	if report.State != runtime.StateUnhealthy {
		t.Errorf("State = %q, want %q", report.State, runtime.StateUnhealthy)
	}
	if strings.Contains(report.ErrorSummary, "super-secret") {
		t.Errorf("ErrorSummary leaked the API key: %q", report.ErrorSummary)
	}
	// A route that exists and reports trouble is not a missing route: the
	// engine's own health signal must not be silently swapped for a weaker
	// one.
	if discovery := mustDiscover(t, rt); containsSubstring(discovery.Warnings, "fall back") {
		t.Errorf("Warnings = %v, want no degradation note for a 5xx", discovery.Warnings)
	}
}

// --- discover ---------------------------------------------------------

func TestDiscoverClaimsOnlyTheOpenAICoreWithoutAVersion(t *testing.T) {
	f := newFakeSGLang(t)
	rt := newRuntime(t, f)

	discovery := mustDiscover(t, rt)
	if discovery.Version != "" {
		t.Errorf("Version = %q, want empty when the private endpoint is absent", discovery.Version)
	}

	tests := []struct {
		capability runtime.Capability
		want       runtime.SupportLevel
		wantSource runtime.CapabilitySource
	}{
		{runtime.CapabilityChat, runtime.SupportSupported, runtime.SourceEndpoint},
		{runtime.CapabilityChatStream, runtime.SupportSupported, runtime.SourceEndpoint},
		{runtime.CapabilityCompletions, runtime.SupportSupported, runtime.SourceEndpoint},
		{runtime.CapabilityTools, runtime.SupportUnknown, ""},
		{runtime.CapabilityStructuredOutput, runtime.SupportUnknown, ""},
		{runtime.CapabilityEmbeddings, runtime.SupportUnknown, ""},
		{runtime.CapabilityVision, runtime.SupportUnknown, ""},
	}
	for _, tt := range tests {
		t.Run(string(tt.capability), func(t *testing.T) {
			ev := discovery.Capabilities.Resolve(tt.capability)
			if ev.Level != tt.want {
				t.Errorf("level = %q, want %q", ev.Level, tt.want)
			}
			if tt.wantSource != "" && ev.Source != tt.wantSource {
				t.Errorf("source = %q, want %q", ev.Source, tt.wantSource)
			}
		})
	}
	if !containsSubstring(discovery.Warnings, "version could not be determined") {
		t.Errorf("Warnings = %v, want a note that the version is unknown", discovery.Warnings)
	}
}

func TestDiscoverUsesServerInfoVersionWhenAvailable(t *testing.T) {
	f := newFakeSGLang(t, func(f *fakeSGLang) {
		f.serverInfo = map[string]any{"version": "0.4.5", "model_path": servedModel}
	})
	rt := newRuntime(t, f)

	discovery := mustDiscover(t, rt)
	if discovery.Version != "0.4.5" {
		t.Fatalf("Version = %q, want %q", discovery.Version, "0.4.5")
	}
	if got := discovery.Capabilities.Resolve(runtime.CapabilityTools).Level; got != runtime.SupportSupported {
		t.Errorf("tools = %q, want supported once the version is known", got)
	}
	if got := discovery.Capabilities.Resolve(runtime.CapabilityStructuredOutput).Level; got != runtime.SupportSupported {
		t.Errorf("structured_output = %q, want supported once the version is known", got)
	}
	if got := discovery.Capabilities.Resolve(runtime.CapabilityEmbeddings).Level; got != runtime.SupportUnknown {
		t.Errorf("embeddings = %q, want unknown even with a version", got)
	}
}

func TestDiscoverSurvivesPrivateEndpointChanges(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*fakeSGLang)
	}{
		{
			name:  "route absent",
			apply: func(f *fakeSGLang) {},
		},
		{
			name:  "route errors",
			apply: func(f *fakeSGLang) { f.serverInfoError = http.StatusInternalServerError },
		},
		{
			name: "fields renamed",
			apply: func(f *fakeSGLang) {
				f.serverInfo = map[string]any{"sglang_version": "0.4.5", "server_args": map[string]any{"tp_size": 2}}
			},
		},
		{
			name: "version is not a string",
			apply: func(f *fakeSGLang) {
				f.serverInfoRaw = `{"version": {"major": 0, "minor": 4}}`
			},
		},
		{
			name:  "body is not json",
			apply: func(f *fakeSGLang) { f.serverInfoRaw = "<html>not json</html>" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeSGLang(t, tt.apply)
			rt := newRuntime(t, f)

			// The private response is a diagnostic, not a contract: whatever
			// it does, Discover still has to publish the models.
			discovery := mustDiscover(t, rt)
			if len(discovery.Models) != 1 || discovery.Models[0].ID != servedModel {
				t.Fatalf("Models = %+v, want the served model", discovery.Models)
			}
			if discovery.Version != "" {
				t.Errorf("Version = %q, want empty", discovery.Version)
			}
			if !containsSubstring(discovery.Warnings, serverInfoEndpoint) {
				t.Errorf("Warnings = %v, want one naming %s", discovery.Warnings, serverInfoEndpoint)
			}
			if got := discovery.Capabilities.Resolve(runtime.CapabilityChat).Level; got != runtime.SupportSupported {
				t.Errorf("chat = %q, want the endpoint evidence to stand on its own", got)
			}
		})
	}
}

func TestDiscoverAppliesCapabilityOverrides(t *testing.T) {
	f := newFakeSGLang(t)
	rt := newRuntime(t, f, func(c *runtime.Config) {
		c.CapabilityOverrides = map[runtime.Capability]runtime.SupportLevel{
			runtime.CapabilityEmbeddings: runtime.SupportSupported,
		}
	})

	discovery := mustDiscover(t, rt)
	ev := discovery.Capabilities.Resolve(runtime.CapabilityEmbeddings)
	if ev.Level != runtime.SupportSupported || ev.Source != runtime.SourceConfigOverride {
		t.Fatalf("embeddings evidence = %+v, want supported from a config override", ev)
	}
	if _, err := rt.Embed(context.Background(), runtime.EmbeddingRequest{Model: servedModel, Input: []string{"hi"}}); err != nil {
		t.Fatalf("Embed after override: %v", err)
	}
}

// --- endpoint discipline ----------------------------------------------

func TestAdapterNeverCallsTheNativeGenerateEndpoint(t *testing.T) {
	f := newFakeSGLang(t, func(f *fakeSGLang) {
		f.serverInfo = map[string]any{"version": "0.4.5"}
	})
	rt := newRuntime(t, f)

	mustProbe(t, rt)
	mustHealth(t, rt)
	mustDiscover(t, rt)
	if _, err := rt.Chat(context.Background(), chatRequest()); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	stream, err := rt.ChatStream(context.Background(), chatRequest())
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	for {
		if _, err := stream.Recv(); err != nil {
			break
		}
	}
	_ = stream.Close()

	want := []string{healthEndpoint, serverInfoEndpoint, "/v1/chat/completions", modelsEndpoint}
	sort.Strings(want)
	got := f.requestedPaths()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("requested paths = %v, want exactly %v", got, want)
	}
	for _, path := range got {
		if path == generateEndpoint {
			t.Error("the adapter called the native /generate endpoint, which is out of scope")
		}
	}
}

// --- request path -----------------------------------------------------

func TestChatBeforeDiscoverIsRejectedAsUnknown(t *testing.T) {
	f := newFakeSGLang(t)
	rt := newRuntime(t, f)

	_, err := rt.Chat(context.Background(), chatRequest())
	requireErrorCode(t, err, runtime.ErrorCapability)
	if !errors.Is(err, runtime.ErrCapabilityUnknown) {
		t.Errorf("expected ErrCapabilityUnknown, got %v", err)
	}
}

func TestChatSucceedsAfterDiscover(t *testing.T) {
	f := newFakeSGLang(t)
	rt := newRuntime(t, f)
	mustDiscover(t, rt)

	resp, err := rt.Chat(context.Background(), chatRequest())
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Message.Content != "pong" {
		t.Errorf("Content = %q, want %q", resp.Message.Content, "pong")
	}
}

func TestChatWithToolsIsRejectedWithoutAKnownVersion(t *testing.T) {
	f := newFakeSGLang(t)
	rt := newRuntime(t, f)
	mustDiscover(t, rt)

	// Endpoint evidence covers the chat core only; tool support genuinely
	// depends on the release and the launch flags.
	_, err := rt.Chat(context.Background(), runtime.ChatRequest{
		Model:    servedModel,
		Messages: []runtime.ChatMessage{{Role: "user", Content: "ping"}},
		Tools:    []runtime.Tool{{Type: "function", Function: runtime.FunctionDefinition{Name: "now"}}},
	})
	requireErrorCode(t, err, runtime.ErrorCapability)
}

func TestEmbedIsRefusedUntilDeclared(t *testing.T) {
	f := newFakeSGLang(t)
	rt := newRuntime(t, f)
	mustDiscover(t, rt)

	_, err := rt.Embed(context.Background(), runtime.EmbeddingRequest{Model: servedModel, Input: []string{"hi"}})
	requireErrorCode(t, err, runtime.ErrorCapability)
	for _, path := range f.requestedPaths() {
		if path == "/v1/embeddings" {
			t.Error("the adapter called /v1/embeddings for an undeclared capability")
		}
	}
}

func TestChatStreamDeliversDeltasAndReleasesTheSlotOnClose(t *testing.T) {
	f := newFakeSGLang(t)
	rt := newRuntime(t, f, func(c *runtime.Config) { c.MaxConcurrent = 1 })
	mustDiscover(t, rt)

	stream, err := rt.ChatStream(context.Background(), chatRequest())
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
	if _, err := rt.Chat(context.Background(), chatRequest()); err != nil {
		t.Fatalf("Chat after stream close: %v", err)
	}
}

func TestUpstreamErrorBodyIsMappedToRuntimeError(t *testing.T) {
	f := newFakeSGLang(t, func(f *fakeSGLang) {
		f.chatStatus = http.StatusTooManyRequests
		f.chatError = "server is overloaded"
	})
	rt := newRuntime(t, f)
	mustDiscover(t, rt)

	_, err := rt.Chat(context.Background(), chatRequest())
	rerr := requireErrorCode(t, err, runtime.ErrorRateLimited)
	if !rerr.Retryable {
		t.Error("Retryable = false, want a rate-limit error to be retryable")
	}
	if !strings.Contains(rerr.Message, "server is overloaded") {
		t.Errorf("Message = %q, want the upstream message preserved", rerr.Message)
	}
}

func TestCloseRejectsSubsequentCalls(t *testing.T) {
	f := newFakeSGLang(t)
	rt := newRuntime(t, f)
	mustDiscover(t, rt)

	if err := rt.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	_, err := rt.Chat(context.Background(), chatRequest())
	requireErrorCode(t, err, runtime.ErrorClosed)
	if _, err := rt.Health(context.Background()); err == nil {
		t.Error("Health after Close: expected an error")
	}
}

func TestNewRejectsAnotherKind(t *testing.T) {
	f := newFakeSGLang(t)
	_, err := sglang.New(runtime.Config{
		ID:      "wrong-kind",
		Kind:    runtime.KindVLLM,
		BaseURL: f.server.URL,
	}.Normalize(), runtime.Dependencies{HTTPClient: f.server.Client()})
	requireErrorCode(t, err, runtime.ErrorInvalidConfig)
}

func TestNewIsUsableAsARegistryFactory(t *testing.T) {
	f := newFakeSGLang(t)
	registry := runtime.NewRegistry()
	if err := registry.Register(runtime.KindSGLang, sglang.New); err != nil {
		t.Fatalf("Register: %v", err)
	}
	rt, err := registry.Create(runtime.Config{
		ID:      "registered-sglang",
		Kind:    runtime.KindSGLang,
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
