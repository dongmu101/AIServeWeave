package controlplaneclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"AIServeWeave/common/apikey"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/controlplaneclient"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

func TestMain(m *testing.M) {
	before := goruntime.NumGoroutine()
	code := m.Run()
	if code == 0 && !goroutineCountSettles(before) {
		os.Stderr.WriteString("leaked goroutines detected after tests completed\n")
		code = 1
	}
	os.Exit(code)
}

func goroutineCountSettles(baseline int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for {
		if goruntime.NumGoroutine() <= baseline {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

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
	return c.now
}

func (c *fakeClock) NewTimer(time.Duration) (<-chan time.Time, func() bool) {
	return make(chan time.Time), func() bool { return true }
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var _ runtime.Clock = (*fakeClock)(nil)

const testToken = "internal-token-value"

// fakeControlPlane is a stand-in for the control plane's internal endpoint. It
// records what it received, which is how the tests assert on what the Gateway
// sent rather than only on what it did with the answer.
//
// fakeControlPlane 是控制面内部端点的替身。它记录自己收到了什么，这正是让测试可以
// 断言「Gateway 发出了什么」，而不只是断言「Gateway 拿到答案后做了什么」的方式。
type fakeControlPlane struct {
	server *httptest.Server

	calls    atomic.Int64
	mu       sync.Mutex
	received []string
	authSeen []string

	// respond answers one call. Replace it to script a failure.
	//
	// respond 应答一次调用。替换它即可脚本化一个失败场景。
	respond func(w http.ResponseWriter, hash string)
}

func newFakeControlPlane(t *testing.T) *fakeControlPlane {
	t.Helper()
	cp := &fakeControlPlane{}
	cp.respond = func(w http.ResponseWriter, _ string) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"tenant_id": "tnt_1", "key_id": "key_1"})
	}
	cp.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cp.calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		var decoded struct {
			Hash string `json:"hash"`
		}
		_ = json.Unmarshal(body, &decoded)

		cp.mu.Lock()
		cp.received = append(cp.received, decoded.Hash)
		cp.authSeen = append(cp.authSeen, r.Header.Get("Authorization"))
		cp.mu.Unlock()

		cp.respond(w, decoded.Hash)
	}))
	t.Cleanup(cp.server.Close)
	return cp
}

func (cp *fakeControlPlane) hashes() []string {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return append([]string(nil), cp.received...)
}

func newVerifier(t *testing.T, cp *fakeControlPlane, clock runtime.Clock) *controlplaneclient.Verifier {
	t.Helper()
	verifier, err := controlplaneclient.New(controlplaneclient.Config{
		Endpoint: cp.server.URL,
		Token:    testToken,
		Clock:    clock,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return verifier
}

func mustKey(t *testing.T) string {
	t.Helper()
	generated, err := apikey.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return generated.Plaintext
}

// TestPlaintextKeyNeverLeavesTheGateway is the security assertion this package
// is shaped around: the control plane must receive the hash and never the key
// a caller presented.
//
// TestPlaintextKeyNeverLeavesTheGateway 是本包围绕其成形的安全断言：控制面必须收到
// 哈希，而绝不能收到调用方所出示的那个 key。
func TestPlaintextKeyNeverLeavesTheGateway(t *testing.T) {
	cp := newFakeControlPlane(t)
	verifier := newVerifier(t, cp, newFakeClock())
	key := mustKey(t)

	if _, err := verifier.Verify(context.Background(), key); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	hashes := cp.hashes()
	if len(hashes) != 1 {
		t.Fatalf("the control plane saw %d calls, want 1", len(hashes))
	}
	if hashes[0] != apikey.Hash(key) {
		t.Errorf("the control plane received %q, want the key's hash", hashes[0])
	}
	secret := strings.TrimPrefix(key, apikey.Prefix)
	if strings.Contains(hashes[0], secret) {
		t.Errorf("the plaintext key reached the control plane")
	}
}

func TestVerifyReturnsTheIdentity(t *testing.T) {
	cp := newFakeControlPlane(t)
	verifier := newVerifier(t, cp, newFakeClock())

	got, err := verifier.Verify(context.Background(), mustKey(t))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	want := httpapi.Identity{TenantID: "tnt_1", KeyID: "key_1"}
	if got != want {
		t.Errorf("identity = %+v, want %+v", got, want)
	}
}

// TestVerificationIsCached asserts the request path does not pay a round trip
// per request, and that the cache expires when it should.
//
// TestVerificationIsCached 断言请求路径不会每个请求付出一次往返，且缓存在该过期时
// 确实过期。
func TestVerificationIsCached(t *testing.T) {
	cp := newFakeControlPlane(t)
	clock := newFakeClock()
	verifier := newVerifier(t, cp, clock)
	key := mustKey(t)

	for range 5 {
		if _, err := verifier.Verify(context.Background(), key); err != nil {
			t.Fatalf("Verify: %v", err)
		}
	}
	if got := cp.calls.Load(); got != 1 {
		t.Errorf("the control plane was called %d times for five verifications, want 1", got)
	}

	clock.Advance(controlplaneclient.DefaultCacheTTL + time.Second)
	if _, err := verifier.Verify(context.Background(), key); err != nil {
		t.Fatalf("Verify after the TTL: %v", err)
	}
	if got := cp.calls.Load(); got != 2 {
		t.Errorf("the control plane was called %d times after the TTL elapsed, want 2", got)
	}
}

// TestCacheExpiryBoundary pins the window a revoked key keeps working in — the
// cost this cache trades for, and the number the service README quotes.
//
// TestCacheExpiryBoundary 钉住一个被吊销的 key 仍能继续工作的窗口——这正是本缓存所
// 换取的代价，也是服务 README 所引用的那个数字。
func TestCacheExpiryBoundary(t *testing.T) {
	cp := newFakeControlPlane(t)
	clock := newFakeClock()
	verifier := newVerifier(t, cp, clock)
	key := mustKey(t)

	if _, err := verifier.Verify(context.Background(), key); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	clock.Advance(controlplaneclient.DefaultCacheTTL - time.Second)
	_, _ = verifier.Verify(context.Background(), key)
	if got := cp.calls.Load(); got != 1 {
		t.Errorf("the cache expired one second early: calls = %d, want 1", got)
	}

	clock.Advance(2 * time.Second)
	_, _ = verifier.Verify(context.Background(), key)
	if got := cp.calls.Load(); got != 2 {
		t.Errorf("the cache outlived its TTL: calls = %d, want 2", got)
	}
}

// TestMalformedKeyCostsNoRoundTrip asserts a scanner cannot make this Gateway
// generate control plane traffic.
//
// TestMalformedKeyCostsNoRoundTrip 断言扫描器无法让本 Gateway 产生通往控制面的流量。
func TestMalformedKeyCostsNoRoundTrip(t *testing.T) {
	cp := newFakeControlPlane(t)
	verifier := newVerifier(t, cp, newFakeClock())

	tests := []string{"", "test", "Bearer", "sk-not-ours", apikey.Prefix + "short"}
	for _, key := range tests {
		t.Run(key, func(t *testing.T) {
			_, err := verifier.Verify(context.Background(), key)
			if !errors.Is(err, httpapi.ErrKeyRejected) {
				t.Errorf("Verify(%q) error = %v, want ErrKeyRejected", key, err)
			}
		})
	}
	if got := cp.calls.Load(); got != 0 {
		t.Errorf("the control plane was called %d times for malformed keys, want 0", got)
	}
}

// TestControlPlaneFailures asserts the distinction the middleware depends on:
// a rejected key is the caller's problem, and everything else is ours.
//
// TestControlPlaneFailures 断言中间件所依赖的那个区分：key 被拒是调用方的问题，其余
// 一切都是我们自己的问题。
func TestControlPlaneFailures(t *testing.T) {
	tests := []struct {
		name string
		// status is what the fake control plane answers.
		//
		// status 是假控制面所应答的状态码。
		status int
		body   string
		// wantRejected states whether the error must wrap ErrKeyRejected,
		// which is what turns into a 401 for the caller. Anything else must
		// not, because it becomes a 503 instead.
		//
		// wantRejected 表示该错误是否必须包装 ErrKeyRejected，那会让调用方收到 401。
		// 其余情况都不得包装它，因为它们要变成 503。
		wantRejected bool
	}{
		{name: "unknown key", status: http.StatusNotFound, wantRejected: true},
		{name: "our token was refused", status: http.StatusUnauthorized, wantRejected: false},
		{name: "our token is forbidden", status: http.StatusForbidden, wantRejected: false},
		{name: "control plane error", status: http.StatusInternalServerError, wantRejected: false},
		{name: "control plane is unavailable", status: http.StatusServiceUnavailable, wantRejected: false},
		{name: "an answer with no tenant", status: http.StatusOK, body: `{"key_id":"key_1"}`, wantRejected: false},
		{name: "an unparseable answer", status: http.StatusOK, body: `not json`, wantRejected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cp := newFakeControlPlane(t)
			cp.respond = func(w http.ResponseWriter, _ string) {
				if tt.body != "" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tt.status)
					_, _ = io.WriteString(w, tt.body)
					return
				}
				w.WriteHeader(tt.status)
			}
			verifier := newVerifier(t, cp, newFakeClock())

			_, err := verifier.Verify(context.Background(), mustKey(t))
			if err == nil {
				t.Fatalf("Verify succeeded, want a failure")
			}
			if got := errors.Is(err, httpapi.ErrKeyRejected); got != tt.wantRejected {
				t.Errorf("errors.Is(err, ErrKeyRejected) = %v, want %v (err = %v)", got, tt.wantRejected, err)
			}
		})
	}
}

// TestFailuresAreNotCached asserts a rejection does not poison the cache: a
// key minted a moment after being probed must work immediately.
//
// TestFailuresAreNotCached 断言一次拒绝不会污染缓存：一个在被试探之后才铸造出来的
// key，必须立刻就能用。
func TestFailuresAreNotCached(t *testing.T) {
	cp := newFakeControlPlane(t)
	var reject atomic.Bool
	reject.Store(true)
	cp.respond = func(w http.ResponseWriter, _ string) {
		if reject.Load() {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"tenant_id": "tnt_1", "key_id": "key_1"})
	}
	verifier := newVerifier(t, cp, newFakeClock())
	key := mustKey(t)

	if _, err := verifier.Verify(context.Background(), key); !errors.Is(err, httpapi.ErrKeyRejected) {
		t.Fatalf("Verify error = %v, want ErrKeyRejected", err)
	}
	reject.Store(false)
	if _, err := verifier.Verify(context.Background(), key); err != nil {
		t.Errorf("a rejection was cached: the key still fails after it became valid: %v", err)
	}
}

func TestNewRejectsBadConfiguration(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		token    string
	}{
		{name: "no endpoint", endpoint: "", token: testToken},
		{name: "no scheme", endpoint: "127.0.0.1:8090", token: testToken},
		{name: "no token", endpoint: "http://127.0.0.1:8090", token: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := controlplaneclient.New(controlplaneclient.Config{
				Endpoint: tt.endpoint,
				Token:    tt.token,
			}); err == nil {
				t.Errorf("New accepted a bad configuration")
			}
		})
	}
}

// TestVerifyIsConcurrencySafe is the -race check: the Gateway verifies from
// every request goroutine at once.
//
// TestVerifyIsConcurrencySafe 是 -race 下的检查：Gateway 会从每个请求协程同时发起校验。
func TestVerifyIsConcurrencySafe(t *testing.T) {
	cp := newFakeControlPlane(t)
	verifier := newVerifier(t, cp, newFakeClock())

	keys := make([]string, 8)
	for i := range keys {
		keys[i] = mustKey(t)
	}

	var wg sync.WaitGroup
	for w := range 16 {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range 50 {
				if _, err := verifier.Verify(context.Background(), keys[(w+i)%len(keys)]); err != nil {
					t.Errorf("Verify: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
}
