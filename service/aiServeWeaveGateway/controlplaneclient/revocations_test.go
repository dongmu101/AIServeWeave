package controlplaneclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/controlplaneclient"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

type watchAnswer struct {
	status int
	body   string
}

type verificationGate struct {
	started chan struct{}
	release chan struct{}
}

type revocationControlPlane struct {
	server *httptest.Server

	verifyCalls atomic.Int64
	reject      atomic.Bool
	watches     chan int64
	answers     chan watchAnswer

	gateMu sync.Mutex
	gate   *verificationGate
}

func newRevocationControlPlane(t *testing.T) *revocationControlPlane {
	t.Helper()
	cp := &revocationControlPlane{
		watches: make(chan int64, 32),
		answers: make(chan watchAnswer, 32),
	}
	cp.server = httptest.NewServer(http.HandlerFunc(cp.serveHTTP))
	t.Cleanup(cp.server.Close)
	return cp
}

func (cp *revocationControlPlane) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/internal/v1/apikeys/verify":
		cp.serveVerification(w, r)
	case "/internal/v1/apikeys/revocations/watch":
		cp.serveWatch(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (cp *revocationControlPlane) serveVerification(w http.ResponseWriter, _ *http.Request) {
	cp.verifyCalls.Add(1)
	cp.gateMu.Lock()
	gate := cp.gate
	cp.gate = nil
	cp.gateMu.Unlock()
	if gate != nil {
		close(gate.started)
		<-gate.release
	}
	if cp.reject.Load() {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"tenant_id": "tnt_1", "key_id": "key_1"})
}

func (cp *revocationControlPlane) serveWatch(w http.ResponseWriter, r *http.Request) {
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if err != nil {
		http.Error(w, "bad after", http.StatusBadRequest)
		return
	}
	select {
	case cp.watches <- after:
	case <-r.Context().Done():
		return
	}
	select {
	case answer := <-cp.answers:
		status := answer.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer.body)
	case <-r.Context().Done():
	}
}

func (cp *revocationControlPlane) answerWatch(generation int64) {
	cp.answers <- watchAnswer{body: "{\"generation\":" + strconv.FormatInt(generation, 10) + "}"}
}

func (cp *revocationControlPlane) failWatch(status int) {
	cp.answers <- watchAnswer{status: status, body: "failure body must not be trusted"}
}

func (cp *revocationControlPlane) waitForWatchAfter(t *testing.T, want int64) {
	t.Helper()
	select {
	case got := <-cp.watches:
		if got != want {
			t.Fatalf("watch after = %d, want %d", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("watch after = absent, want %d", want)
	}
}

func (cp *revocationControlPlane) waitForWatchAfterContext(ctx context.Context, t *testing.T, want int64) {
	t.Helper()
	select {
	case got := <-cp.watches:
		if got != want {
			t.Fatalf("watch after = %d, want %d", got, want)
		}
	case <-ctx.Done():
		t.Fatalf("watch after = absent, want %d", want)
	}
}

func (cp *revocationControlPlane) blockNextVerification() *verificationGate {
	gate := &verificationGate{started: make(chan struct{}), release: make(chan struct{})}
	cp.gateMu.Lock()
	cp.gate = gate
	cp.gateMu.Unlock()
	return gate
}

type watcherClock struct {
	mu     sync.Mutex
	now    time.Time
	timers chan time.Duration
}

func newWatcherClock() *watcherClock {
	return &watcherClock{
		now:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		timers: make(chan time.Duration, 16),
	}
}

// Now returns the fixed cache timestamp.
//
// Now 返回固定的缓存时间戳。
func (c *watcherClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// NewTimer records retry delays without advancing real time.
//
// NewTimer 在不推进真实时间的情况下记录重试等待。
func (c *watcherClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	ch := make(chan time.Time)
	c.timers <- d
	return ch, func() bool { return true }
}

var _ runtime.Clock = (*watcherClock)(nil)

func newRevocationVerifier(t *testing.T, cp *revocationControlPlane, clock runtime.Clock) *controlplaneclient.Verifier {
	t.Helper()
	verifier, err := controlplaneclient.New(controlplaneclient.Config{
		Endpoint: cp.server.URL,
		Token:    testToken,
		Clock:    clock,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New error = %v, want nil", err)
	}
	return verifier
}

func runRevocationWatcher(verifier *controlplaneclient.Verifier) (context.CancelFunc, <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		verifier.RunRevocationWatch(ctx)
	}()
	return cancel, done
}

func mustVerify(t *testing.T, verifier *controlplaneclient.Verifier, key string) {
	t.Helper()
	if _, err := verifier.Verify(context.Background(), key); err != nil {
		t.Fatalf("Verify error = %v, want nil", err)
	}
}

func runningHealthyVerifier(t *testing.T) (*revocationControlPlane, *controlplaneclient.Verifier, *watcherClock, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	cp := newRevocationControlPlane(t)
	clock := newWatcherClock()
	verifier := newRevocationVerifier(t, cp, clock)
	cancel, done := runRevocationWatcher(verifier)
	cp.waitForWatchAfter(t, 0)
	cp.answerWatch(0)
	cp.waitForWatchAfter(t, 0)
	return cp, verifier, clock, cancel, done
}

// TestCacheRequiresHealthyRevocationWatch proves startup requests bypass the
// positive cache until an authoritative generation is observed.
//
// TestCacheRequiresHealthyRevocationWatch 证明启动后的请求会绕过正向缓存，直到观测到
// 权威 generation。
func TestCacheRequiresHealthyRevocationWatch(t *testing.T) {
	cp := newRevocationControlPlane(t)
	clock := newWatcherClock()
	verifier := newRevocationVerifier(t, cp, clock)
	key := mustKey(t)

	mustVerify(t, verifier, key)
	mustVerify(t, verifier, key)
	if got := cp.verifyCalls.Load(); got != 2 {
		t.Fatalf("verify calls before sync = %d, want 2", got)
	}

	cancel, done := runRevocationWatcher(verifier)
	cp.waitForWatchAfter(t, 0)
	cp.answerWatch(0)
	cp.waitForWatchAfter(t, 0)
	mustVerify(t, verifier, key)
	mustVerify(t, verifier, key)
	if got := cp.verifyCalls.Load(); got != 3 {
		t.Errorf("verify calls after sync = %d, want 3", got)
	}
	cancel()
	<-done
}

// TestGenerationChangeClearsCache proves both increasing and reset generations
// invalidate a locally cached positive result.
//
// TestGenerationChangeClearsCache 证明递增与回退重置的 generation 都会使本地正向
// 缓存失效。
func TestGenerationChangeClearsCache(t *testing.T) {
	tests := []struct {
		name       string
		generation int64
	}{
		{name: "advance", generation: 1},
		{name: "reset", generation: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cp, verifier, _, cancel, done := runningHealthyVerifier(t)
			defer func() { cancel(); <-done }()
			if tt.name == "reset" {
				cp.answerWatch(5)
				cp.waitForWatchAfter(t, 5)
			}
			key := mustKey(t)
			mustVerify(t, verifier, key)
			mustVerify(t, verifier, key)
			cp.answerWatch(tt.generation)
			cp.waitForWatchAfter(t, tt.generation)
			mustVerify(t, verifier, key)
			if got := cp.verifyCalls.Load(); got != 2 {
				t.Errorf("verify calls after generation %d = %d, want 2", tt.generation, got)
			}
		})
	}
}

// TestWatchFailureBlocksStaleRefill proves a verification begun before a
// watch failure cannot repopulate the cache after it has been disabled.
//
// TestWatchFailureBlocksStaleRefill 证明通知故障前开始的校验，不能在缓存被禁用后将
// 旧结果重新填回。
func TestWatchFailureBlocksStaleRefill(t *testing.T) {
	cp, verifier, clock, cancel, done := runningHealthyVerifier(t)
	defer func() { cancel(); <-done }()
	key := mustKey(t)
	gate := cp.blockNextVerification()
	firstDone := make(chan error, 1)
	go func() {
		_, err := verifier.Verify(context.Background(), key)
		firstDone <- err
	}()
	<-gate.started
	cp.failWatch(http.StatusServiceUnavailable)
	if got := <-clock.timers; got != controlplaneclient.DefaultRevocationRetryMin {
		t.Fatalf("retry delay = %s, want %s", got, controlplaneclient.DefaultRevocationRetryMin)
	}
	close(gate.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("in-flight Verify error = %v, want nil", err)
	}
	mustVerify(t, verifier, key)
	if got := cp.verifyCalls.Load(); got != 2 {
		t.Errorf("verify calls after stale response = %d, want 2", got)
	}
}

// TestInvalidWatchResponseDisablesCache proves an authenticated but malformed
// or unsuccessful notification response cannot leave positive caching active.
//
// TestInvalidWatchResponseDisablesCache 证明一条虽已认证但畸形或失败的通知响应，
// 不能让正向缓存继续启用。
func TestInvalidWatchResponseDisablesCache(t *testing.T) {
	tests := []struct {
		name   string
		answer watchAnswer
	}{
		{name: "missing generation", answer: watchAnswer{body: `{}`}},
		{name: "negative generation", answer: watchAnswer{body: `{"generation":-1}`}},
		{name: "malformed JSON", answer: watchAnswer{body: `{"generation":`}},
		{name: "oversized response", answer: watchAnswer{body: strings.Repeat("x", maxTestRevocationResponseBytes+1)}},
		{name: "unauthorized", answer: watchAnswer{status: http.StatusUnauthorized}},
		{name: "not found", answer: watchAnswer{status: http.StatusNotFound}},
		{name: "server failure", answer: watchAnswer{status: http.StatusInternalServerError}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cp, verifier, clock, cancel, done := runningHealthyVerifier(t)
			defer func() { cancel(); <-done }()
			key := mustKey(t)
			mustVerify(t, verifier, key)
			mustVerify(t, verifier, key)
			cp.answers <- tt.answer
			select {
			case got := <-clock.timers:
				if got != controlplaneclient.DefaultRevocationRetryMin {
					t.Fatalf("retry delay = %s, want %s", got, controlplaneclient.DefaultRevocationRetryMin)
				}
			case <-time.After(time.Second):
				t.Fatal("invalid watch response did not enter retry")
			}
			mustVerify(t, verifier, key)
			mustVerify(t, verifier, key)
			if got := cp.verifyCalls.Load(); got != 3 {
				t.Errorf("verify calls while unhealthy = %d, want 3", got)
			}
		})
	}
}

const maxTestRevocationResponseBytes = 1 << 10

// TestSilentWatchTimeoutDisablesCache proves a half-open notification request
// cannot leave positive caching active beyond the configured detection bound.
//
// TestSilentWatchTimeoutDisablesCache 证明半开通知请求不能让正向缓存继续启用超过
// 配置的检测边界。
func TestSilentWatchTimeoutDisablesCache(t *testing.T) {
	cp := newRevocationControlPlane(t)
	clock := newWatcherClock()
	verifier, err := controlplaneclient.New(controlplaneclient.Config{
		Endpoint:          cp.server.URL,
		Token:             testToken,
		Clock:             clock,
		RevocationTimeout: 25 * time.Millisecond,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New error = %v, want nil", err)
	}
	cancel, done := runRevocationWatcher(verifier)
	defer func() { cancel(); <-done }()
	cp.waitForWatchAfter(t, 0)
	cp.answerWatch(0)
	cp.waitForWatchAfter(t, 0)

	key := mustKey(t)
	mustVerify(t, verifier, key)
	mustVerify(t, verifier, key)
	select {
	case got := <-clock.timers:
		if got != controlplaneclient.DefaultRevocationRetryMin {
			t.Fatalf("retry delay = %s, want %s", got, controlplaneclient.DefaultRevocationRetryMin)
		}
	case <-time.After(time.Second):
		t.Fatal("silent watch did not time out")
	}
	mustVerify(t, verifier, key)
	mustVerify(t, verifier, key)
	if got := cp.verifyCalls.Load(); got != 3 {
		t.Errorf("verify calls while timeout-unhealthy = %d, want 3", got)
	}
}

// TestEveryVerifierInvalidatesWithinHealthyDeadline proves one generation
// reaches independent Gateway caches and meets the connected-path deadline.
//
// TestEveryVerifierInvalidatesWithinHealthyDeadline 证明同一个 generation 会到达
// 相互独立的 Gateway 缓存，并满足连接健康时的时限。
func TestEveryVerifierInvalidatesWithinHealthyDeadline(t *testing.T) {
	cp := newRevocationControlPlane(t)
	first := newRevocationVerifier(t, cp, newWatcherClock())
	second := newRevocationVerifier(t, cp, newWatcherClock())
	stopFirst, firstDone := runRevocationWatcher(first)
	stopSecond, secondDone := runRevocationWatcher(second)
	defer func() {
		stopFirst()
		stopSecond()
		<-firstDone
		<-secondDone
	}()

	cp.waitForWatchAfter(t, 0)
	cp.waitForWatchAfter(t, 0)
	cp.answerWatch(0)
	cp.answerWatch(0)
	cp.waitForWatchAfter(t, 0)
	cp.waitForWatchAfter(t, 0)
	key := mustKey(t)
	for _, verifier := range []*controlplaneclient.Verifier{first, second} {
		mustVerify(t, verifier, key)
		mustVerify(t, verifier, key)
	}
	if got := cp.verifyCalls.Load(); got != 2 {
		t.Fatalf("verify calls before revocation = %d, want 2", got)
	}

	cp.answerWatch(1)
	cp.answerWatch(1)
	deadline, cancelDeadline := context.WithTimeout(context.Background(), time.Second)
	defer cancelDeadline()
	cp.waitForWatchAfterContext(deadline, t, 1)
	cp.waitForWatchAfterContext(deadline, t, 1)
	cp.reject.Store(true)
	for i, verifier := range []*controlplaneclient.Verifier{first, second} {
		if _, err := verifier.Verify(context.Background(), key); !errors.Is(err, httpapi.ErrKeyRejected) {
			t.Errorf("verifier %d error = %v, want ErrKeyRejected", i, err)
		}
	}
}
