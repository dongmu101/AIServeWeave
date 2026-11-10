package comfyui_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/internal/runtimetest"
	"AIServeWeave/common/runtime/workflow/comfyui"
)

// promptGate wraps the HTTP transport so a test can hold POST /prompt open,
// and make the first one fail, without touching the fake server.
type promptGate struct {
	next http.RoundTripper

	entered chan struct{} // receives one value per POST /prompt that arrives
	release chan struct{} // closed to let held requests proceed

	failFirst atomic.Bool
	arrivals  atomic.Int32
}

func newPromptGate(next http.RoundTripper) *promptGate {
	return &promptGate{next: next, entered: make(chan struct{}, 64), release: make(chan struct{})}
}

func (g *promptGate) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || req.URL.Path != "/prompt" {
		return g.next.RoundTrip(req)
	}
	n := g.arrivals.Add(1)
	g.entered <- struct{}{}
	select {
	case <-g.release:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	if n == 1 && g.failFirst.Load() {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Status:     "400 Bad Request",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rejected"}}`)),
			Request:    req,
		}, nil
	}
	return g.next.RoundTrip(req)
}

func newGatedRuntime(t *testing.T, f *fakeComfy) (*comfyui.Runtime, *promptGate) {
	t.Helper()
	gate := newPromptGate(f.server.Client().Transport)
	cfg := runtime.Config{ID: "test-comfyui", Kind: runtime.KindComfyUI, BaseURL: f.server.URL, MaxConcurrent: 8}
	rt, err := comfyui.New(cfg.Normalize(), runtime.Dependencies{
		HTTPClient: &http.Client{Transport: gate},
		WSDialer:   newScriptedWS(8), // concurrent first Submits may each dial before one wins
		Clock:      runtimetest.NewClock(time.Unix(1700000000, 0)),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	adapter := rt.(*comfyui.Runtime)
	mustDiscover(t, adapter)
	return adapter, gate
}

func waitEntered(t *testing.T, gate *promptGate) {
	t.Helper()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no POST /prompt reached the transport")
	}
}

type submitResult struct {
	run runtime.WorkflowRun
	err error
}

func TestConcurrentSubmitsWithOneKeySubmitOnce(t *testing.T) {
	f := newFakeComfy(t)
	rt, gate := newGatedRuntime(t, f)

	const callers = 8
	req := runtime.WorkflowRequest{Template: workflowTemplate(), IdempotencyKey: "job-1"}
	results := make(chan submitResult, callers)
	for range callers {
		go func() {
			run, err := rt.Submit(context.Background(), req)
			results <- submitResult{run, err}
		}()
	}

	// One submission reaches ComfyUI and is held; give the other callers time
	// to arrive and, with the old lookup-then-record code, slip past it.
	waitEntered(t, gate)
	time.Sleep(100 * time.Millisecond)
	close(gate.release)

	for range callers {
		res := <-results
		if res.err != nil {
			t.Fatalf("Submit: %v", res.err)
		}
		if res.run.ID != testRunID {
			t.Errorf("run ID = %q, want the single submission's %q", res.run.ID, testRunID)
		}
	}
	if got := f.promptSubmissions(); got != 1 {
		t.Errorf("POST /prompt reached ComfyUI %d times for one idempotency key, want 1", got)
	}
	if got := gate.arrivals.Load(); got != 1 {
		t.Errorf("%d submissions were attempted, want 1", got)
	}
}

func TestConcurrentSubmitsWithDifferentKeysAreIndependent(t *testing.T) {
	f := newFakeComfy(t)
	rt, gate := newGatedRuntime(t, f)

	done := make(chan error, 2)
	for _, key := range []string{"a", "b"} {
		go func() {
			_, err := rt.Submit(context.Background(), runtime.WorkflowRequest{Template: workflowTemplate(), IdempotencyKey: key})
			done <- err
		}()
	}
	// Both are held in flight at once: the key of one does not serialize the other.
	waitEntered(t, gate)
	waitEntered(t, gate)
	close(gate.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	if got := f.promptSubmissions(); got != 2 {
		t.Errorf("POST /prompt count = %d, want 2", got)
	}
}

func TestFailedSubmitIsNotCachedAndWaitersRetry(t *testing.T) {
	f := newFakeComfy(t)
	rt, gate := newGatedRuntime(t, f)
	gate.failFirst.Store(true)

	req := runtime.WorkflowRequest{Template: workflowTemplate(), IdempotencyKey: "job-2"}
	first := make(chan submitResult, 1)
	go func() {
		run, err := rt.Submit(context.Background(), req)
		first <- submitResult{run, err}
	}()
	waitEntered(t, gate)

	// A second caller joins while the first is in flight and will be told to
	// wait for it.
	second := make(chan submitResult, 1)
	go func() {
		run, err := rt.Submit(context.Background(), req)
		second <- submitResult{run, err}
	}()
	time.Sleep(100 * time.Millisecond)
	if gate.arrivals.Load() != 1 {
		t.Fatal("the second caller submitted instead of waiting")
	}
	close(gate.release)

	// The first submission is rejected; that failure must not be handed to the
	// waiter as if it were its own, nor remembered as a success.
	if res := <-first; res.err == nil {
		t.Fatal("the rejected submission reported success")
	}
	res := <-second
	if res.err != nil {
		t.Fatalf("the waiter inherited the first caller's failure: %v", res.err)
	}
	if res.run.ID != testRunID {
		t.Errorf("waiter run ID = %q, want %q", res.run.ID, testRunID)
	}
	if got := f.promptSubmissions(); got != 1 {
		t.Errorf("POST /prompt reached ComfyUI %d times, want 1 (the rejected attempt never arrived)", got)
	}

	// And the success is now cached.
	again, err := rt.Submit(context.Background(), req)
	if err != nil || again.ID != testRunID {
		t.Fatalf("Submit after the retry = %+v, %v, want the cached run", again, err)
	}
	if got := f.promptSubmissions(); got != 1 {
		t.Errorf("a cached key submitted again: %d POSTs", got)
	}
}

func TestSubmitWaiterHonoursItsOwnContext(t *testing.T) {
	f := newFakeComfy(t)
	rt, gate := newGatedRuntime(t, f)

	req := runtime.WorkflowRequest{Template: workflowTemplate(), IdempotencyKey: "job-3"}
	first := make(chan submitResult, 1)
	go func() {
		run, err := rt.Submit(context.Background(), req)
		first <- submitResult{run, err}
	}()
	waitEntered(t, gate)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := rt.Submit(ctx, req)
	rerr := requireErrorCode(t, err, runtime.ErrorTimeout)
	if !rerr.Retryable {
		t.Error("giving up on a wait is safe to retry with the same key and must say so")
	}

	// The cancelled waiter did not disturb the owner.
	close(gate.release)
	res := <-first
	if res.err != nil || res.run.ID != testRunID {
		t.Fatalf("owner = %+v, %v, want the run", res.run, res.err)
	}
	if got := f.promptSubmissions(); got != 1 {
		t.Errorf("POST /prompt count = %d, want 1", got)
	}
}

func TestOwnerCancellationDoesNotFailWaiters(t *testing.T) {
	f := newFakeComfy(t)
	rt, gate := newGatedRuntime(t, f)

	req := runtime.WorkflowRequest{Template: workflowTemplate(), IdempotencyKey: "job-4"}
	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	var ownerErr atomic.Value
	var ownerWG sync.WaitGroup
	ownerWG.Add(1)
	go func() {
		defer ownerWG.Done()
		if _, err := rt.Submit(ownerCtx, req); err != nil {
			ownerErr.Store(err)
		}
	}()
	waitEntered(t, gate)

	waiter := make(chan submitResult, 1)
	go func() {
		run, err := rt.Submit(context.Background(), req)
		waiter <- submitResult{run, err}
	}()
	time.Sleep(100 * time.Millisecond)

	// The owner gives up; its context error must not reach the waiter, which
	// takes over the submission.
	cancelOwner()
	ownerWG.Wait()
	if ownerErr.Load() == nil {
		t.Fatal("the cancelled owner reported success")
	}
	waitEntered(t, gate)
	close(gate.release)

	res := <-waiter
	if res.err != nil || res.run.ID != testRunID {
		t.Fatalf("waiter = %+v, %v, want to take over and succeed", res.run, res.err)
	}
	if got := f.promptSubmissions(); got != 1 {
		t.Errorf("POST /prompt reached ComfyUI %d times, want 1", got)
	}
}
