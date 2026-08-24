package localdiscovery

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/ollama"
	"AIServeWeave/common/runtime/vllm"
)

// newFakeOllamaServer answers just enough of the Ollama protocol for
// Probe+Discover to succeed against an empty model list: GET /api/version
// and GET /api/tags. It also answers GET /version (which real Ollama does
// not serve) with a counted 404, so a test can prove a vLLM probe against
// this address was retried without needing a second real backend.
func newFakeOllamaServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var versionPathHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"version": "0.1.0"})
	})
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"models": []any{}})
	})
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		versionPathHits.Add(1)
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &versionPathHits
}

// newFakeVLLMServer answers just enough of the vLLM protocol for
// Probe+Discover to succeed against an empty model list: GET /version and
// GET /v1/models.
func newFakeVLLMServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"version": "0.1.0"})
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []any{}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// newTestManager returns a real runtime.Manager wired with the Ollama and
// vLLM factories — a real Manager, not a fake, because Probe/Validate is
// exactly what this package's tests need to exercise.
func newTestManager(t *testing.T) runtime.Manager {
	t.Helper()
	registry := runtime.NewRegistry()
	if err := registry.Register(runtime.KindOllama, ollama.New); err != nil {
		t.Fatalf("register ollama: %v", err)
	}
	if err := registry.Register(runtime.KindVLLM, vllm.New); err != nil {
		t.Fatalf("register vllm: %v", err)
	}
	manager := runtime.NewManager(registry, runtime.Dependencies{
		HTTPClient: http.DefaultClient,
		Clock:      runtime.NewSystemClock(),
		Logger:     slog.New(slog.DiscardHandler),
		Metrics:    discardMetrics{},
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Close(ctx)
	})
	return manager
}

func TestScanOnceRegistersAnAnsweringCandidate(t *testing.T) {
	server, _ := newFakeOllamaServer(t)
	manager := newTestManager(t)
	scanner, err := New(Config{
		Manager:    manager,
		Candidates: []Candidate{{Kind: runtime.KindOllama, BaseURL: server.URL}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	scanner.scanOnce(context.Background())

	snaps := manager.Snapshot()
	if len(snaps) != 1 {
		t.Fatalf("Snapshot() = %d instances, want 1", len(snaps))
	}
	if snaps[0].Descriptor.BaseURL != server.URL || snaps[0].Descriptor.Kind != runtime.KindOllama {
		t.Errorf("registered descriptor = %+v, want kind=ollama base_url=%s", snaps[0].Descriptor, server.URL)
	}
}

func TestScanOnceSkipsACandidateWithNothingListening(t *testing.T) {
	// A loopback port nothing is listening on: httptest.NewServer picks one
	// and Close releases it immediately, so the address is guaranteed free
	// and guaranteed to refuse the connection rather than hang.
	closed := httptest.NewServer(http.NotFoundHandler())
	addr := closed.URL
	closed.Close()

	manager := newTestManager(t)
	scanner, err := New(Config{
		Manager:    manager,
		Candidates: []Candidate{{Kind: runtime.KindOllama, BaseURL: addr}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	scanner.scanOnce(context.Background())

	if got := len(manager.Snapshot()); got != 0 {
		t.Errorf("Snapshot() = %d instances, want 0 (nothing should have registered)", got)
	}
}

func TestScanOnceDoesNotDuplicateAManuallyRegisteredInstance(t *testing.T) {
	server, _ := newFakeOllamaServer(t)
	manager := newTestManager(t)
	if err := manager.Add(context.Background(), runtime.Config{
		ID: "my-ollama", Kind: runtime.KindOllama, BaseURL: server.URL,
	}); err != nil {
		t.Fatalf("manual Add: %v", err)
	}

	scanner, err := New(Config{
		Manager:    manager,
		Candidates: []Candidate{{Kind: runtime.KindOllama, BaseURL: server.URL}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	scanner.scanOnce(context.Background())

	snaps := manager.Snapshot()
	if len(snaps) != 1 {
		t.Fatalf("Snapshot() = %d instances, want exactly the 1 manually registered one", len(snaps))
	}
	if snaps[0].Descriptor.ID != "my-ollama" {
		t.Errorf("Descriptor.ID = %q, want the manually chosen id to survive untouched", snaps[0].Descriptor.ID)
	}
}

func TestScanOnceRegistersMultipleDistinctCandidates(t *testing.T) {
	ollamaSrv, _ := newFakeOllamaServer(t)
	vllmSrv := newFakeVLLMServer(t)
	manager := newTestManager(t)
	scanner, err := New(Config{
		Manager: manager,
		Candidates: []Candidate{
			{Kind: runtime.KindOllama, BaseURL: ollamaSrv.URL},
			{Kind: runtime.KindVLLM, BaseURL: vllmSrv.URL},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	scanner.scanOnce(context.Background())

	if got := len(manager.Snapshot()); got != 2 {
		t.Fatalf("Snapshot() = %d instances, want 2", got)
	}
}

func TestRunScansAgainAfterEachInterval(t *testing.T) {
	// This candidate is deliberately probed as the wrong kind, so it never
	// successfully registers and Run must keep retrying it every interval.
	// Counting hits on the fake server's /version path is what proves a
	// second scan actually happened.
	server, versionHits := newFakeOllamaServer(t)
	manager := newTestManager(t)
	clock := newFakeClock()
	scanner, err := New(Config{
		Manager:    manager,
		Candidates: []Candidate{{Kind: runtime.KindVLLM, BaseURL: server.URL}},
		Interval:   time.Second,
		Clock:      clock,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scanner.Run(ctx) }()

	waitFor(t, "the first scan to probe the candidate", func() bool { return versionHits.Load() >= 1 })
	before := versionHits.Load()

	clock.waitForPendingTimer(t)
	clock.Advance(time.Second)
	waitFor(t, "a second scan to probe the candidate again", func() bool { return versionHits.Load() > before })

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after ctx was canceled")
	}
	if got := len(manager.Snapshot()); got != 0 {
		t.Errorf("Snapshot() = %d instances, want 0 (the mismatched-kind candidate must never register)", got)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// discardMetrics satisfies runtime.Metrics without recording anything.
type discardMetrics struct{}

func (discardMetrics) Counter(string, map[string]string) runtime.Counter { return discardInstrument{} }
func (discardMetrics) Gauge(string, map[string]string) runtime.Gauge     { return discardInstrument{} }
func (discardMetrics) Histogram(string, map[string]string) runtime.Histogram {
	return discardInstrument{}
}

type discardInstrument struct{}

func (discardInstrument) Add(float64)     {}
func (discardInstrument) Set(float64)     {}
func (discardInstrument) Observe(float64) {}

// fakeClock is a runtime.Clock whose time only advances when the test tells
// it to, mirroring the pattern used throughout this codebase (e.g.
// service/aiServeWeaveGateway/internal/gatewaytest.Clock) for exercising a
// periodic loop without sleeping in real time.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	c       chan time.Time
	stopped bool
	fired   bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), c: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	return t.c, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if t.fired || t.stopped {
			return false
		}
		t.stopped = true
		return true
	}
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var due []*fakeTimer
	kept := c.timers[:0]
	for _, t := range c.timers {
		if !t.stopped && !t.fired && !t.at.After(now) {
			t.fired = true
			due = append(due, t)
			continue
		}
		if !t.stopped && !t.fired {
			kept = append(kept, t)
		}
	}
	c.timers = kept
	c.mu.Unlock()

	for _, t := range due {
		t.c <- now
	}
}

// waitForPendingTimer blocks until at least one live timer is registered, so
// a test's Advance call cannot race ahead of Run's goroutine reaching its
// clock.NewTimer call.
func (c *fakeClock) waitForPendingTimer(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		pending := len(c.timers) > 0
		c.mu.Unlock()
		if pending {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for a pending timer")
}

var _ runtime.Clock = (*fakeClock)(nil)
