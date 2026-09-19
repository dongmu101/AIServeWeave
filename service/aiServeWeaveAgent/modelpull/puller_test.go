package modelpull

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/modelpullstatus"
	"AIServeWeave/common/runtime"
)

// fakeClock is a minimal runtime.Clock: Puller only reads Now(), it never
// arms a timer, so NewTimer is unused but must still exist to satisfy the
// interface.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Millisecond)
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	panic("modelpull: Puller does not use Clock.NewTimer")
}

// advance moves the clock forward by d, for tests that need to cross a
// Ledger's Period boundary (ledger_test.go) rather than rely on Now()'s
// per-call millisecond drift.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func statusOf(t *testing.T, p *Puller, name string) modelpullstatus.Status {
	t.Helper()
	for _, st := range p.Snapshot() {
		if st.Name == name {
			return st
		}
	}
	t.Fatalf("Snapshot has no status for %q", name)
	return modelpullstatus.Status{}
}

func waitForState(t *testing.T, p *Puller, name string, want modelpullstatus.State) modelpullstatus.Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last modelpullstatus.Status
	for time.Now().Before(deadline) {
		last = statusOf(t, p, name)
		if last.State == want {
			return last
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q to reach state %v; last status = %+v", name, want, last)
	return last
}

func TestPuller_TriggerUnknownName(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer srv.Close()

	p := NewPuller(context.Background(), Config{Allowlist: []string{srv.URL}}, nil, newFakeClock())
	p.Trigger([]string{"ghost"})

	got := waitForState(t, p, "ghost", modelpullstatus.StateFailed)
	if got.Reason != modelpullstatus.ReasonUnknownName {
		t.Fatalf("Reason = %v, want ReasonUnknownName", got.Reason)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0 (an unknown name must never touch the network)", requests)
	}
}

func TestPuller_TriggerDedupesInFlight(t *testing.T) {
	content := []byte("some model bytes")
	digest := sha256Hex(content)
	release := make(chan struct{})
	requests := 0
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		<-release
		w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	spec := Spec{Name: "m1", SourceURL: srv.URL, SHA256: digest, SizeBytes: int64(len(content)), TargetPath: filepath.Join(dir, "m1.bin")}
	p := NewPuller(context.Background(), Config{Allowlist: []string{srv.URL}}, []Spec{spec}, newFakeClock())

	p.Trigger([]string{"m1"})
	waitForState(t, p, "m1", modelpullstatus.StateDownloading)
	p.Trigger([]string{"m1"}) // must not start a second request
	close(release)

	waitForState(t, p, "m1", modelpullstatus.StateDone)
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Fatalf("requests = %d, want 1 (retriggering an in-flight name must not start a second download)", requests)
	}
}

func TestPuller_ProcessesQueueSequentially(t *testing.T) {
	content := []byte("bytes")
	digest := sha256Hex(content)

	var mu sync.Mutex
	var order []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		order = append(order, r.URL.Path)
		mu.Unlock()
		w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	specs := []Spec{
		{Name: "a", SourceURL: srv.URL + "/a", SHA256: digest, SizeBytes: int64(len(content)), TargetPath: filepath.Join(dir, "a.bin")},
		{Name: "b", SourceURL: srv.URL + "/b", SHA256: digest, SizeBytes: int64(len(content)), TargetPath: filepath.Join(dir, "b.bin")},
	}
	p := NewPuller(context.Background(), Config{Allowlist: []string{srv.URL}}, specs, newFakeClock())
	p.Trigger([]string{"a", "b"})

	waitForState(t, p, "a", modelpullstatus.StateDone)
	waitForState(t, p, "b", modelpullstatus.StateDone)

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 {
		t.Fatalf("order = %v, want 2 requests", order)
	}
}

func TestPuller_ProgressVisibleDuringDownload(t *testing.T) {
	const chunkSize = 5
	content := []byte("0123456789ABCDE") // 3 chunks of 5
	digest := sha256Hex(content)
	proceed := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Write(content[:chunkSize])
		if flusher != nil {
			flusher.Flush()
		}
		<-proceed
		w.Write(content[chunkSize:])
	}))
	defer srv.Close()

	dir := t.TempDir()
	spec := Spec{Name: "m1", SourceURL: srv.URL, SHA256: digest, SizeBytes: int64(len(content)), TargetPath: filepath.Join(dir, "m1.bin")}
	p := NewPuller(context.Background(), Config{Allowlist: []string{srv.URL}}, []Spec{spec}, newFakeClock())
	p.Trigger([]string{"m1"})

	deadline := time.Now().Add(5 * time.Second)
	var partial modelpullstatus.Status
	for time.Now().Before(deadline) {
		partial = statusOf(t, p, "m1")
		if partial.BytesDownloaded > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if partial.BytesDownloaded == 0 {
		t.Fatal("timed out waiting for partial progress to be observed")
	}
	if partial.State != modelpullstatus.StateDownloading {
		t.Fatalf("State = %v while mid-download, want StateDownloading", partial.State)
	}
	if partial.BytesDownloaded >= int64(len(content)) {
		t.Fatalf("BytesDownloaded = %d observed before the handler released the rest of the body, want < %d", partial.BytesDownloaded, len(content))
	}

	close(proceed)
	final := waitForState(t, p, "m1", modelpullstatus.StateDone)
	if final.BytesDownloaded != int64(len(content)) {
		t.Fatalf("final BytesDownloaded = %d, want %d", final.BytesDownloaded, len(content))
	}
}

func TestPuller_QuotaSharedWithinSessionFreshAcrossSessions(t *testing.T) {
	content := []byte("0123456789") // 10 bytes each
	digest := sha256Hex(content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	specs := []Spec{
		{Name: "a", SourceURL: srv.URL, SHA256: digest, SizeBytes: int64(len(content)), TargetPath: filepath.Join(dir, "a.bin")},
		{Name: "b", SourceURL: srv.URL, SHA256: digest, SizeBytes: int64(len(content)), TargetPath: filepath.Join(dir, "b.bin")},
	}
	// Budget covers exactly one of the two 10-byte entries triggered together.
	p := NewPuller(context.Background(), Config{Allowlist: []string{srv.URL}, QuotaBytes: 10}, specs, newFakeClock())
	p.Trigger([]string{"a", "b"})

	waitForState(t, p, "a", modelpullstatus.StateDone)
	failed := waitForState(t, p, "b", modelpullstatus.StateFailed)
	if failed.Reason != modelpullstatus.ReasonQuotaExceeded {
		t.Fatalf("b.Reason = %v, want ReasonQuotaExceeded (a should have exhausted the shared session budget)", failed.Reason)
	}

	// A new Trigger starts a new worker session with a fresh budget.
	p.Trigger([]string{"b"})
	retried := waitForState(t, p, "b", modelpullstatus.StateDone)
	if retried.BytesDownloaded != int64(len(content)) {
		t.Fatalf("b.BytesDownloaded after retry = %d, want %d", retried.BytesDownloaded, len(content))
	}
}

func TestPuller_FailureReasons(t *testing.T) {
	content := []byte("expected bytes")
	digest := sha256Hex(content)

	mux := http.NewServeMux()
	mux.HandleFunc("/wrong-checksum", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not what was promised"))
	})
	mux.HandleFunc("/teapot", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	specs := []Spec{
		{Name: "checksum", SourceURL: srv.URL + "/wrong-checksum", SHA256: digest, TargetPath: filepath.Join(dir, "checksum.bin")},
		{Name: "status", SourceURL: srv.URL + "/teapot", SHA256: digest, TargetPath: filepath.Join(dir, "status.bin")},
		{Name: "outside", SourceURL: "https://example.invalid/outside", SHA256: digest, TargetPath: filepath.Join(dir, "outside.bin")},
	}
	// Only the server under test is allowlisted, so "outside" is rejected
	// before any request — matching how the manifest itself would be
	// misconfigured, not a network failure.
	p := NewPuller(context.Background(), Config{Allowlist: []string{srv.URL}}, specs, newFakeClock())
	p.Trigger([]string{"checksum", "status", "outside"})

	tests := []struct {
		name       string
		wantReason modelpullstatus.FailureReason
	}{
		{"checksum", modelpullstatus.ReasonChecksumMismatch},
		{"status", modelpullstatus.ReasonUnexpectedStatus},
		{"outside", modelpullstatus.ReasonNotAllowlisted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := waitForState(t, p, tt.name, modelpullstatus.StateFailed)
			if got.Reason != tt.wantReason {
				t.Fatalf("Reason = %v, want %v", got.Reason, tt.wantReason)
			}
		})
	}
}

func TestPuller_SnapshotSortedByName(t *testing.T) {
	specs := []Spec{
		{Name: "zeta", SourceURL: "https://example.invalid/z", SHA256: "0"}, // invalid, never triggered
		{Name: "alpha", SourceURL: "https://example.invalid/a", SHA256: "0"},
	}
	p := NewPuller(context.Background(), Config{}, specs, newFakeClock())
	snap := p.Snapshot()
	if len(snap) != 2 || snap[0].Name != "alpha" || snap[1].Name != "zeta" {
		t.Fatalf("Snapshot() = %+v, want [alpha, zeta]", snap)
	}
}

func TestPuller_TriggerOllamaKind(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		writeNDJSON(t, w, []map[string]any{
			{"status": "downloading", "total": int64(10), "completed": int64(5)},
			{"status": "success"},
		})
	}))
	defer srv.Close()

	specs := []Spec{{Name: "qwen3-coder:30b", Kind: KindOllama}}
	p := NewPuller(context.Background(), Config{OllamaBaseURL: srv.URL}, specs, newFakeClock())
	p.Trigger([]string{"qwen3-coder:30b"})

	got := waitForState(t, p, "qwen3-coder:30b", modelpullstatus.StateDone)
	if got.BytesDownloaded != 5 || got.BytesTotal != 10 {
		t.Fatalf("status = %+v, want BytesDownloaded=5 BytesTotal=10", got)
	}
	if gotBody["model"] != "qwen3-coder:30b" {
		t.Fatalf("request body model = %v, want qwen3-coder:30b", gotBody["model"])
	}
}

func TestPuller_TriggerOllamaKindUnconfigured(t *testing.T) {
	specs := []Spec{{Name: "qwen3-coder:30b", Kind: KindOllama}}
	p := NewPuller(context.Background(), Config{}, specs, newFakeClock())
	p.Trigger([]string{"qwen3-coder:30b"})

	got := waitForState(t, p, "qwen3-coder:30b", modelpullstatus.StateFailed)
	if got.Reason != modelpullstatus.ReasonOllamaUnconfigured {
		t.Fatalf("Reason = %v, want ReasonOllamaUnconfigured", got.Reason)
	}
}

func TestPuller_TriggerOllamaKindServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeNDJSON(t, w, []map[string]any{{"error": "model not found"}})
	}))
	defer srv.Close()

	specs := []Spec{{Name: "ghost:latest", Kind: KindOllama}}
	p := NewPuller(context.Background(), Config{OllamaBaseURL: srv.URL}, specs, newFakeClock())
	p.Trigger([]string{"ghost:latest"})

	got := waitForState(t, p, "ghost:latest", modelpullstatus.StateFailed)
	if got.Reason != modelpullstatus.ReasonOllamaPullFailed {
		t.Fatalf("Reason = %v, want ReasonOllamaPullFailed", got.Reason)
	}
}

func TestPuller_MaxConcurrencyRunsDownloadsInParallel(t *testing.T) {
	const n = 3
	content := []byte("bytes")
	digest := sha256Hex(content)

	var mu sync.Mutex
	inFlight := 0
	maxInFlight := 0
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()

		<-release

		mu.Lock()
		inFlight--
		mu.Unlock()
		w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	specs := make([]Spec, n)
	names := make([]string, n)
	for i := range specs {
		name := fmt.Sprintf("m%d", i)
		names[i] = name
		specs[i] = Spec{Name: name, SourceURL: srv.URL, SHA256: digest, SizeBytes: int64(len(content)), TargetPath: filepath.Join(dir, name+".bin")}
	}

	p := NewPuller(context.Background(), Config{Allowlist: []string{srv.URL}, MaxConcurrency: n}, specs, newFakeClock())
	p.Trigger(names)

	for _, name := range names {
		waitForState(t, p, name, modelpullstatus.StateDownloading)
	}
	close(release)
	for _, name := range names {
		waitForState(t, p, name, modelpullstatus.StateDone)
	}

	mu.Lock()
	defer mu.Unlock()
	if maxInFlight != n {
		t.Fatalf("maxInFlight = %d, want %d: MaxConcurrency should let all %d downloads run at once", maxInFlight, n, n)
	}
}

var _ runtime.Clock = (*fakeClock)(nil)
