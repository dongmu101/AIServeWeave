package vllm_test

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
	"AIServeWeave/common/runtime/vllm"
)

const servedModel = "meta-llama/Llama-3.1-8B-Instruct"

// --- fake vLLM server -------------------------------------------------

type fakeVLLM struct {
	server *httptest.Server
	prefix string

	mu            sync.Mutex
	version       string
	omitVersion   bool
	versionStatus int
	healthStatus  int
	models        []string
	paths         []string
	authHeaders   []string
	chatStatus    int
	chatError     string
}

func newFakeVLLM(t *testing.T, opts ...func(*fakeVLLM)) *fakeVLLM {
	t.Helper()
	f := &fakeVLLM{version: "0.6.3", models: []string{servedModel}}
	for _, opt := range opts {
		opt(f)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		// Any route the adapter is not supposed to touch lands here; the
		// 404 keeps a stray call visible in both the path log and the test.
		writeJSONError(w, http.StatusNotFound, "unknown route")
	})
	mux.HandleFunc(f.prefix+"/version", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.versionStatus != 0 {
			writeJSONError(w, f.versionStatus, "version unavailable")
			return
		}
		if f.omitVersion {
			// vLLM has changed this payload's shape across releases; an
			// unrecognized body must not read as "not a vLLM server".
			writeJSON(w, map[string]any{"vllm_version": f.version})
			return
		}
		writeJSON(w, map[string]any{"version": f.version})
	})
	mux.HandleFunc(f.prefix+"/health", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		f.mu.Lock()
		status := f.healthStatus
		f.mu.Unlock()
		if status != 0 {
			writeJSONError(w, status, "engine is dead")
			return
		}
		w.WriteHeader(http.StatusOK) // vLLM answers /health with an empty body
	})
	mux.HandleFunc(f.prefix+"/v1/models", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		f.mu.Lock()
		defer f.mu.Unlock()
		data := make([]map[string]any, 0, len(f.models))
		for _, id := range f.models {
			data = append(data, map[string]any{"id": id, "object": "model"})
		}
		writeJSON(w, map[string]any{"object": "list", "data": data})
	})
	mux.HandleFunc(f.prefix+"/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc(f.prefix+"/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
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

func (f *fakeVLLM) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.Path)
	f.authHeaders = append(f.authHeaders, r.Header.Get("Authorization"))
}

func (f *fakeVLLM) requestedPaths() []string {
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

func (f *fakeVLLM) lastAuthHeader() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.authHeaders) == 0 {
		return ""
	}
	return f.authHeaders[len(f.authHeaders)-1]
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

func newRuntime(t *testing.T, f *fakeVLLM, mutate ...func(*runtime.Config)) *vllm.Runtime {
	t.Helper()
	cfg := runtime.Config{
		ID:      "test-vllm",
		Kind:    runtime.KindVLLM,
		BaseURL: f.server.URL + f.prefix,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	rt, err := vllm.New(cfg.Normalize(), runtime.Dependencies{
		HTTPClient: f.server.Client(),
		Clock:      runtimetest.NewClock(time.Unix(1700000000, 0)),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	adapter, ok := rt.(*vllm.Runtime)
	if !ok {
		t.Fatalf("New returned %T, want *vllm.Runtime", rt)
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

func mustDiscover(t *testing.T, rt *vllm.Runtime) runtime.Discovery {
	t.Helper()
	discovery, err := rt.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	return discovery
}

func chatRequest() runtime.ChatRequest {
	return runtime.ChatRequest{
		Model:    servedModel,
		Messages: []runtime.ChatMessage{{Role: "user", Content: "ping"}},
	}
}

// --- probe ------------------------------------------------------------

func TestProbeRequiresBothVersionAndModels(t *testing.T) {
	f := newFakeVLLM(t)
	rt := newRuntime(t, f)

	result, err := rt.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if result.Kind != runtime.KindVLLM || !result.IdentityVerified {
		t.Errorf("Probe = %+v, want a verified vLLM identity", result)
	}
	if result.Version != "0.6.3" {
		t.Errorf("Version = %q, want %q", result.Version, "0.6.3")
	}
	if !strings.Contains(result.Evidence, versionEndpoint) || !strings.Contains(result.Evidence, "1 model(s)") {
		t.Errorf("Evidence = %q, want it to cite both endpoints", result.Evidence)
	}
}

func TestProbeRejectsBackendWithoutVersionEndpoint(t *testing.T) {
	// SGLang serves /v1/models but not /version; adopting it as vLLM would
	// mean reporting a version and capabilities it never claimed.
	f := newFakeVLLM(t, func(f *fakeVLLM) { f.versionStatus = http.StatusNotFound })
	rt := newRuntime(t, f)

	_, err := rt.Probe(context.Background())
	rerr := requireErrorCode(t, err, runtime.ErrorProbeMismatch)
	if !strings.Contains(rerr.Message, versionEndpoint) {
		t.Errorf("Message = %q, want it to name %s", rerr.Message, versionEndpoint)
	}
}

func TestProbeAcceptsVersionPayloadWithoutVersionField(t *testing.T) {
	f := newFakeVLLM(t, func(f *fakeVLLM) { f.omitVersion = true })
	rt := newRuntime(t, f)

	result, err := rt.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !result.IdentityVerified {
		t.Error("IdentityVerified = false; the route answering is the vLLM-specific evidence")
	}
	if result.Version != "" {
		t.Errorf("Version = %q, want empty when the field is absent", result.Version)
	}
	if !strings.Contains(result.Evidence, "(absent)") {
		t.Errorf("Evidence = %q, want it to record the missing version", result.Evidence)
	}
}

func TestProbeKeepsServerErrorsDistinctFromMismatch(t *testing.T) {
	f := newFakeVLLM(t, func(f *fakeVLLM) { f.versionStatus = http.StatusInternalServerError })
	rt := newRuntime(t, f)

	_, err := rt.Probe(context.Background())
	requireErrorCode(t, err, runtime.ErrorUpstream)
}

// --- health -----------------------------------------------------------

func TestHealthUsesHealthEndpoint(t *testing.T) {
	f := newFakeVLLM(t)
	rt := newRuntime(t, f)

	report, err := rt.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if report.State != runtime.StateHealthy {
		t.Errorf("State = %q, want %q", report.State, runtime.StateHealthy)
	}
	if got := f.requestedPaths(); len(got) != 1 || got[0] != healthEndpoint {
		t.Errorf("requested paths = %v, want only %s", got, healthEndpoint)
	}
}

func TestHealthReportsUnhealthyWithRedactedSummary(t *testing.T) {
	f := newFakeVLLM(t, func(f *fakeVLLM) { f.healthStatus = http.StatusServiceUnavailable })
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
}

// --- discover ---------------------------------------------------------

func TestDiscoverReportsModelsWithProfileCapabilities(t *testing.T) {
	f := newFakeVLLM(t)
	rt := newRuntime(t, f)

	discovery := mustDiscover(t, rt)
	if discovery.Version != "0.6.3" {
		t.Errorf("Version = %q, want %q", discovery.Version, "0.6.3")
	}
	if len(discovery.Models) != 1 || discovery.Models[0].ID != servedModel {
		t.Fatalf("Models = %+v, want the single served model", discovery.Models)
	}

	tests := []struct {
		capability runtime.Capability
		want       runtime.SupportLevel
	}{
		{runtime.CapabilityChat, runtime.SupportSupported},
		{runtime.CapabilityChatStream, runtime.SupportSupported},
		{runtime.CapabilityTools, runtime.SupportSupported},
		{runtime.CapabilityStructuredOutput, runtime.SupportSupported},
		// vLLM only serves embeddings for pooling models, and no endpoint
		// reports which kind is loaded.
		{runtime.CapabilityEmbeddings, runtime.SupportUnknown},
		{runtime.CapabilityVision, runtime.SupportUnknown},
		{runtime.CapabilityResponses, runtime.SupportUnknown},
		{runtime.CapabilityParallelToolCalls, runtime.SupportUnknown},
	}
	for _, tt := range tests {
		t.Run(string(tt.capability), func(t *testing.T) {
			if got := discovery.Capabilities.Resolve(tt.capability).Level; got != tt.want {
				t.Errorf("runtime %s = %q, want %q", tt.capability, got, tt.want)
			}
			if got := discovery.Models[0].Capabilities.Resolve(tt.capability).Level; got != tt.want {
				t.Errorf("model %s = %q, want %q", tt.capability, got, tt.want)
			}
		})
	}
}

func TestDiscoverOnOlderVersionKeepsAdvancedCapabilitiesUnknown(t *testing.T) {
	f := newFakeVLLM(t, func(f *fakeVLLM) { f.version = "0.4.2" })
	rt := newRuntime(t, f)

	discovery := mustDiscover(t, rt)
	if got := discovery.Capabilities.Resolve(runtime.CapabilityChat).Level; got != runtime.SupportSupported {
		t.Errorf("chat = %q, want supported", got)
	}
	if got := discovery.Capabilities.Resolve(runtime.CapabilityTools).Level; got != runtime.SupportUnknown {
		t.Errorf("tools = %q, want unknown below the profile floor", got)
	}
}

func TestDiscoverWarnsWhenVersionIsAbsent(t *testing.T) {
	f := newFakeVLLM(t, func(f *fakeVLLM) { f.omitVersion = true })
	rt := newRuntime(t, f)

	discovery := mustDiscover(t, rt)
	if len(discovery.Warnings) == 0 {
		t.Fatal("Warnings is empty, want one about the missing version")
	}
	if got := discovery.Capabilities.Resolve(runtime.CapabilityChat).Level; got != runtime.SupportUnknown {
		t.Errorf("chat = %q, want unknown when the version could not be read", got)
	}

	// The instance is not stranded: an operator can declare what the
	// version could not prove.
	_, err := rt.Chat(context.Background(), chatRequest())
	requireErrorCode(t, err, runtime.ErrorCapability)
}

func TestDiscoverAppliesCapabilityOverrides(t *testing.T) {
	f := newFakeVLLM(t, func(f *fakeVLLM) { f.omitVersion = true })
	rt := newRuntime(t, f, func(c *runtime.Config) {
		c.CapabilityOverrides = map[runtime.Capability]runtime.SupportLevel{
			runtime.CapabilityChat:       runtime.SupportSupported,
			runtime.CapabilityEmbeddings: runtime.SupportSupported,
		}
	})

	discovery := mustDiscover(t, rt)
	ev := discovery.Capabilities.Resolve(runtime.CapabilityChat)
	if ev.Level != runtime.SupportSupported || ev.Source != runtime.SourceConfigOverride {
		t.Fatalf("chat evidence = %+v, want supported from a config override", ev)
	}
	if _, err := rt.Chat(context.Background(), chatRequest()); err != nil {
		t.Fatalf("Chat after override: %v", err)
	}
}

// --- endpoint discipline ----------------------------------------------

func TestAdapterTouchesOnlyTheFiveAllowedEndpoints(t *testing.T) {
	f := newFakeVLLM(t)
	rt := newRuntime(t, f, func(c *runtime.Config) {
		c.CapabilityOverrides = map[runtime.Capability]runtime.SupportLevel{
			runtime.CapabilityEmbeddings: runtime.SupportSupported,
		}
	})

	if _, err := rt.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if _, err := rt.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	mustDiscover(t, rt)
	if _, err := rt.Chat(context.Background(), chatRequest()); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if _, err := rt.Embed(context.Background(), runtime.EmbeddingRequest{Model: servedModel, Input: []string{"hi"}}); err != nil {
		t.Fatalf("Embed: %v", err)
	}

	// Administrative routes such as /v1/load_lora_adapter, /sleep and
	// /shutdown can disturb a server shared with other tenants; the adapter
	// must never call anything outside this list.
	want := []string{healthEndpoint, "/v1/chat/completions", "/v1/embeddings", modelsEndpoint, versionEndpoint}
	sort.Strings(want)
	got := f.requestedPaths()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("requested paths = %v, want exactly %v", got, want)
	}
}

func TestPathPrefixIsPreservedOnEveryEndpoint(t *testing.T) {
	f := newFakeVLLM(t, func(f *fakeVLLM) { f.prefix = "/inference/vllm" })
	rt := newRuntime(t, f)

	if _, err := rt.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if _, err := rt.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	mustDiscover(t, rt)
	if _, err := rt.Chat(context.Background(), chatRequest()); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	for _, path := range f.requestedPaths() {
		if !strings.HasPrefix(path, "/inference/vllm/") {
			t.Errorf("requested %q, want the configured path prefix preserved", path)
		}
	}
}

func TestAPIKeyIsSentAndKeptOutOfErrors(t *testing.T) {
	f := newFakeVLLM(t, func(f *fakeVLLM) {
		f.chatStatus = http.StatusUnauthorized
		f.chatError = "invalid api key: super-secret"
	})
	rt := newRuntime(t, f, func(c *runtime.Config) { c.APIKey = "super-secret" })
	mustDiscover(t, rt)

	if got := f.lastAuthHeader(); got != "Bearer super-secret" {
		t.Errorf("Authorization = %q, want the configured key as a bearer token", got)
	}

	_, err := rt.Chat(context.Background(), chatRequest())
	rerr := requireErrorCode(t, err, runtime.ErrorUnauthorized)
	if strings.Contains(rerr.Message, "super-secret") {
		t.Errorf("error message leaked the API key: %q", rerr.Message)
	}
	if !strings.Contains(rerr.Message, "[REDACTED]") {
		t.Errorf("error message = %q, want the key replaced with [REDACTED]", rerr.Message)
	}
}

func TestUpstreamErrorBodyIsMappedToRuntimeError(t *testing.T) {
	f := newFakeVLLM(t, func(f *fakeVLLM) {
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

// --- request path -----------------------------------------------------

func TestChatBeforeDiscoverIsRejectedAsUnknown(t *testing.T) {
	f := newFakeVLLM(t)
	rt := newRuntime(t, f)

	_, err := rt.Chat(context.Background(), chatRequest())
	requireErrorCode(t, err, runtime.ErrorCapability)
	if !errors.Is(err, runtime.ErrCapabilityUnknown) {
		t.Errorf("expected ErrCapabilityUnknown, got %v", err)
	}
}

func TestChatSucceedsAfterDiscover(t *testing.T) {
	f := newFakeVLLM(t)
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

func TestEmbedIsRefusedUntilDeclared(t *testing.T) {
	f := newFakeVLLM(t)
	rt := newRuntime(t, f)
	mustDiscover(t, rt)

	_, err := rt.Embed(context.Background(), runtime.EmbeddingRequest{Model: servedModel, Input: []string{"hi"}})
	requireErrorCode(t, err, runtime.ErrorCapability)
	if !errors.Is(err, runtime.ErrCapabilityUnknown) {
		t.Errorf("expected ErrCapabilityUnknown, got %v", err)
	}
	for _, path := range f.requestedPaths() {
		if path == "/v1/embeddings" {
			t.Error("the adapter called /v1/embeddings for an undeclared capability")
		}
	}
}

func TestChatStreamDeliversDeltasAndReleasesTheSlotOnClose(t *testing.T) {
	f := newFakeVLLM(t)
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

func TestCloseRejectsSubsequentCalls(t *testing.T) {
	f := newFakeVLLM(t)
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
	if _, err := rt.Probe(context.Background()); err == nil {
		t.Error("Probe after Close: expected an error")
	}
}

func TestNewRejectsAnotherKind(t *testing.T) {
	f := newFakeVLLM(t)
	_, err := vllm.New(runtime.Config{
		ID:      "wrong-kind",
		Kind:    runtime.KindOllama,
		BaseURL: f.server.URL,
	}.Normalize(), runtime.Dependencies{HTTPClient: f.server.Client()})
	requireErrorCode(t, err, runtime.ErrorInvalidConfig)
}

func TestNewIsUsableAsARegistryFactory(t *testing.T) {
	f := newFakeVLLM(t)
	registry := runtime.NewRegistry()
	if err := registry.Register(runtime.KindVLLM, vllm.New); err != nil {
		t.Fatalf("Register: %v", err)
	}
	rt, err := registry.Create(runtime.Config{
		ID:      "registered-vllm",
		Kind:    runtime.KindVLLM,
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

const (
	versionEndpoint = "/version"
	healthEndpoint  = "/health"
	modelsEndpoint  = "/v1/models"
)

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
