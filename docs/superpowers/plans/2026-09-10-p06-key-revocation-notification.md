# P06 Key Revocation Notification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Push ControlPlane verification-generation changes to every Gateway so healthy replicas reject revoked API Keys within one second and notification failures synchronously disable local positive caching.

**Architecture:** Redis atomically increments and publishes an authoritative generation. An InternalToken-protected long-poll endpoint exposes the cursor; each Gateway runs one cancellable watcher that clears or disables its verifier cache. A process-local epoch prevents an in-flight old verification from repopulating the cache after invalidation.

**Tech Stack:** Go 1.27, go-zero REST, go-redis/v9, standard-library HTTP/JSON/concurrency, existing `runtime.Clock`.

**Spec:** `docs/superpowers/specs/2026-09-10-p06-key-revocation-notification-design.md`

## Global Constraints

- Preserve all current P05 working-tree changes. Never reset, discard, reformat, or stage an unrelated hunk; use `git add -p` where P05 and P06 overlap.
- Do not add a schema, migration, proto field, third-party dependency, Console change, per-Key payload, or command-line flag.
- Notifications contain only a non-negative generation; never Key plaintext/hash, tenant/user identifiers, prompts, headers, or workflow JSON.
- Gateway caching starts disabled. Every watch error synchronously clears and disables it before retry.
- Healthy propagation is ≤1 second; silent disconnection detection is ≤5 seconds; the accepted database-commit-before-Redis-invalidation crash window remains bounded by `-key-cache-ttl` (30 seconds by default).
- ControlPlane wait is 2 seconds, Gateway watch timeout 5 seconds, retry backoff 100 milliseconds to 2 seconds.
- New and changed comments are English/Chinese pairs. Time tests use injected clocks, contexts, and channels, not `time.Sleep`.

## File Map

- Create `service/aiServeWeaveControlPlane/internal/handler/revocations.go` and `revocations_test.go`.
- Create `service/aiServeWeaveControlPlane/e2e/revocations_test.go` to pin the mounted route and guard.
- Modify ControlPlane `internal/cache/cache.go`, `cache_live_test.go`, `internal/svc/servicecontext.go`, `internal/types/types.go`, and `internal/handler/routes.go`.
- Create Gateway `controlplaneclient/revocations.go` and `revocations_test.go`.
- Modify Gateway `controlplaneclient/verifier.go`, `verifier_test.go`, and `main.go`.
- Update `STATUS.md`, `README.md`, `CHANGELOG.md`, both service READMEs, and both ControlPlane YAML examples.

---

### Task 1: ControlPlane generation feed and protected endpoint

**Files:**
- Create: `service/aiServeWeaveControlPlane/internal/handler/revocations.go`
- Create: `service/aiServeWeaveControlPlane/internal/handler/revocations_test.go`
- Create: `service/aiServeWeaveControlPlane/e2e/revocations_test.go`
- Modify: `service/aiServeWeaveControlPlane/internal/cache/cache.go`
- Modify: `service/aiServeWeaveControlPlane/internal/cache/cache_live_test.go`
- Modify: `service/aiServeWeaveControlPlane/internal/svc/servicecontext.go`
- Modify: `service/aiServeWeaveControlPlane/internal/types/types.go`
- Modify: `service/aiServeWeaveControlPlane/internal/handler/routes.go`

**Interfaces:**
- Consumes: existing Redis `generationKey`, `logic.Invalidator`, `requireSharedSecret`, `writeJSON`, and `writeError`.
- Produces: `svc.RevocationSource.WatchGeneration(context.Context, int64, time.Duration) (int64, error)` and `GET /internal/v1/apikeys/revocations/watch?after=N` returning `{"generation":N}`.

- [ ] **Step 1: Write failing HTTP contract tests**

Create a same-package fake implementing the wished-for source:

```go
type fakeRevocationSource struct {
    generation int64
    err        error
    after      int64
    wait       time.Duration
    called     bool
}

func (f *fakeRevocationSource) WatchGeneration(_ context.Context, after int64, wait time.Duration) (int64, error) {
    f.called, f.after, f.wait = true, after, wait
    return f.generation, f.err
}
```

Table-drive these literal cases through `requireSharedSecret(..., watchAPIKeyRevocations(ctx))`:

```go
tests := []struct {
    name       string
    target     string
    token      string
    source     *fakeRevocationSource
    wantStatus int
    wantBody   string
    wantCalled bool
}{
    {name: "changed", target: "/internal/v1/apikeys/revocations/watch?after=41", token: revocationTestToken, source: &fakeRevocationSource{generation: 42}, wantStatus: 200, wantBody: "{\"generation\":42}\n", wantCalled: true},
    {name: "heartbeat", target: "/internal/v1/apikeys/revocations/watch?after=42", token: revocationTestToken, source: &fakeRevocationSource{generation: 42}, wantStatus: 200, wantBody: "{\"generation\":42}\n", wantCalled: true},
    {name: "missing", target: "/internal/v1/apikeys/revocations/watch", token: revocationTestToken, source: &fakeRevocationSource{}, wantStatus: 400, wantBody: "{\"error\":\"exactly one non-negative after value is required\"}\n"},
    {name: "repeated", target: "/internal/v1/apikeys/revocations/watch?after=1&after=2", token: revocationTestToken, source: &fakeRevocationSource{}, wantStatus: 400, wantBody: "{\"error\":\"exactly one non-negative after value is required\"}\n"},
    {name: "negative", target: "/internal/v1/apikeys/revocations/watch?after=-1", token: revocationTestToken, source: &fakeRevocationSource{}, wantStatus: 400, wantBody: "{\"error\":\"exactly one non-negative after value is required\"}\n"},
    {name: "overflow", target: "/internal/v1/apikeys/revocations/watch?after=9223372036854775808", token: revocationTestToken, source: &fakeRevocationSource{}, wantStatus: 400, wantBody: "{\"error\":\"exactly one non-negative after value is required\"}\n"},
    {name: "redis unavailable", target: "/internal/v1/apikeys/revocations/watch?after=1", token: revocationTestToken, source: &fakeRevocationSource{err: errors.New("unavailable")}, wantStatus: 503, wantBody: "{\"error\":\"revocation notification unavailable\"}\n", wantCalled: true},
    {name: "missing token", target: "/internal/v1/apikeys/revocations/watch?after=1", source: &fakeRevocationSource{generation: 2}, wantStatus: 401, wantBody: "{\"error\":\"missing bearer token\"}\n"},
}
```

For called cases, assert the literal `after` and `revocationWatchWait == 2*time.Second`. Add a cancellation case whose fake waits on `ctx.Done()` and signals return through a channel.

```go
func TestWatchAPIKeyRevocationsCancelsSource(t *testing.T) {
    entered := make(chan struct{})
    returned := make(chan struct{})
    source := revocationSourceFunc(func(ctx context.Context, _ int64, _ time.Duration) (int64, error) {
        close(entered)
        <-ctx.Done()
        close(returned)
        return 0, ctx.Err()
    })
    svcCtx := &svc.ServiceContext{Revocations: source}
    requestCtx, cancel := context.WithCancel(context.Background())
    request := httptest.NewRequest(http.MethodGet, "/internal/v1/apikeys/revocations/watch?after=0", nil).WithContext(requestCtx)
    done := make(chan struct{})
    go func() {
        watchAPIKeyRevocations(svcCtx).ServeHTTP(httptest.NewRecorder(), request)
        close(done)
    }()
    <-entered
    cancel()
    <-returned
    <-done
}
```

Define `type revocationSourceFunc func(context.Context, int64, time.Duration) (int64, error)` and give it the one-line `WatchGeneration` adapter used above.

- [ ] **Step 2: Verify RED**

```bash
go test ./service/aiServeWeaveControlPlane/internal/handler -run 'TestWatchAPIKeyRevocations' -count=1
```

Expected: compile failure for the missing source, handler, and wait constant.

- [ ] **Step 3: Add the narrow interface, response, handler, and route**

Add to `servicecontext.go`:

```go
// RevocationSource exposes the durable generation behind API Key cache invalidation.
//
// RevocationSource 暴露 API Key 缓存失效背后的持久 generation。
type RevocationSource interface {
    WatchGeneration(ctx context.Context, after int64, wait time.Duration) (int64, error)
}
```

Add `Revocations RevocationSource` to `ServiceContext`. Add to `types.go`:

```go
// RevocationGenerationResponse is one notification cursor or heartbeat.
//
// RevocationGenerationResponse 是一次失效通知游标或心跳。
type RevocationGenerationResponse struct {
    Generation int64 `json:"generation"`
}
```

Create `revocations.go`:

```go
const revocationWatchWait = 2 * time.Second

func watchAPIKeyRevocations(ctx *svc.ServiceContext) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        values, exists := r.URL.Query()["after"]
        if !exists || len(values) != 1 {
            writeError(w, http.StatusBadRequest, "exactly one non-negative after value is required")
            return
        }
        after, err := strconv.ParseInt(values[0], 10, 64)
        if err != nil || after < 0 {
            writeError(w, http.StatusBadRequest, "exactly one non-negative after value is required")
            return
        }
        if ctx.Revocations == nil {
            writeError(w, http.StatusServiceUnavailable, "revocation notification unavailable")
            return
        }
        generation, err := ctx.Revocations.WatchGeneration(r.Context(), after, revocationWatchWait)
        if err != nil || generation < 0 {
            writeError(w, http.StatusServiceUnavailable, "revocation notification unavailable")
            return
        }
        writeJSON(w, http.StatusOK, types.RevocationGenerationResponse{Generation: generation})
    }
}
```

Mount it separately with `rest.WithTimeout(revocationWatchWait+time.Second)` and the existing InternalToken middleware.

Create the route-level test after the handler is green:

```go
type fixedRevocations int64

func (f fixedRevocations) WatchGeneration(context.Context, int64, time.Duration) (int64, error) {
    return int64(f), nil
}

func TestRevocationWatchRouteAndGuard(t *testing.T) {
    h := newHarness(t)
    h.svcCtx.Revocations = fixedRevocations(7)
    var got types.RevocationGenerationResponse
    if status := h.call(http.MethodGet, "/internal/v1/apikeys/revocations/watch?after=6", internalToken, nil, &got); status != http.StatusOK || got.Generation != 7 {
        t.Fatalf("watch = (status %d, generation %d), want (%d, 7)", status, got.Generation, http.StatusOK)
    }
    if status := h.call(http.MethodGet, "/internal/v1/apikeys/revocations/watch?after=6", "wrong-token", nil, nil); status != http.StatusUnauthorized {
        t.Errorf("wrong-token status = %d, want %d", status, http.StatusUnauthorized)
    }
}
```

- [ ] **Step 4: Write failing Redis publication tests**

Change `cache_live_test.go` to package `cache` so it can inspect the private channel. Preserve the stale-`Put` test and add:

```go
func TestLiveInvalidationPublishesToEveryWatcher(t *testing.T) {
    client := liveRedisClient(t)
    v := NewWithClient(client, time.Minute)
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    before, err := v.Generation(ctx)
    if err != nil {
        t.Fatalf("Generation error = %v, want nil", err)
    }
    results := make(chan int64, 2)
    for range 2 {
        go func() {
            generation, err := v.WatchGeneration(ctx, before, time.Minute)
            if err != nil {
                results <- -1
                return
            }
            results <- generation
        }()
    }
    waitForSubscribers(t, ctx, client, notificationChannel, 2)
    v.InvalidateAll(ctx)
    for i := 0; i < 2; i++ {
        if got := <-results; got != before+1 {
            t.Errorf("watcher %d generation = %d, want %d", i, got, before+1)
        }
    }
}

func TestLiveWatcherCatchesUpWithoutPubSub(t *testing.T) {
    client := liveRedisClient(t)
    v := NewWithClient(client, time.Minute)
    before, _ := v.Generation(context.Background())
    v.InvalidateAll(context.Background())
    got, err := v.WatchGeneration(context.Background(), before, time.Minute)
    if err != nil || got != before+1 {
        t.Fatalf("WatchGeneration = (%d, %v), want (%d, nil)", got, err, before+1)
    }
}
```

`waitForSubscribers` calls `PubSubNumSub` until count 2 or context expiry and yields with `runtime.Gosched()`; it does not sleep. Add a test that closes the client and proves an outstanding watch returns an error.

```go
func TestLiveWatchReturnsWhenClientCloses(t *testing.T) {
    client := liveRedisClient(t)
    v := NewWithClient(client, time.Minute)
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    after, err := v.Generation(ctx)
    if err != nil {
        t.Fatalf("Generation error = %v, want nil", err)
    }
    result := make(chan error, 1)
    go func() {
        _, err := v.WatchGeneration(ctx, after, time.Minute)
        result <- err
    }()
    waitForSubscribers(t, ctx, client, notificationChannel, 1)
    if err := client.Close(); err != nil {
        t.Fatalf("Close error = %v, want nil", err)
    }
    if err := <-result; err == nil {
        t.Fatal("WatchGeneration error = nil, want client-closed failure")
    }
}
```

- [ ] **Step 5: Verify Redis RED**

```bash
AISW_REDIS_TEST_ADDR=127.0.0.1:6379 go test ./service/aiServeWeaveControlPlane/internal/cache -run 'TestLive(InvalidationPublishesToEveryWatcher|WatcherCatchesUpWithoutPubSub)' -count=1
```

Expected: compile failure for `Generation`, `WatchGeneration`, and `notificationChannel`.

- [ ] **Step 6: Implement atomic generation publication and watching**

Add:

```go
const notificationChannel = keyPrefix + "generation:changed"

var invalidateScript = redis.NewScript(`
local generation = redis.call('INCR', KEYS[1])
redis.call('PUBLISH', ARGV[1], generation)
return generation
`)
```

Both invalidation methods call one private helper running that script. Implement `Generation` so missing generation means zero, while Redis failures and negative values return errors.

```go
func (v *Verifications) invalidate(ctx context.Context) {
    if v == nil {
        return
    }
    _ = invalidateScript.Run(ctx, v.client, []string{generationKey}, notificationChannel).Err()
}

func (v *Verifications) Generation(ctx context.Context) (int64, error) {
    if v == nil {
        return 0, errors.New("cache: revocation notification unavailable")
    }
    generation, err := v.client.Get(ctx, generationKey).Int64()
    if errors.Is(err, redis.Nil) {
        return 0, nil
    }
    if err != nil {
        return 0, errors.New("cache: reading verification generation")
    }
    if generation < 0 {
        return 0, errors.New("cache: negative verification generation")
    }
    return generation, nil
}
```

`Invalidate` and `InvalidateAll` both delegate to `invalidate(ctx)`; keep the unused hash parameter because `logic.Invalidator` already defines it.

Implement `WatchGeneration` in this exact order:

```go
subscription := v.client.Subscribe(ctx, notificationChannel)
defer subscription.Close()
if _, err := subscription.Receive(ctx); err != nil {
    return 0, err
}
generation, err := v.Generation(ctx)
if err != nil || generation != after {
    return generation, err
}
waitCtx, cancel := context.WithTimeout(ctx, wait)
defer cancel()
for {
    if _, err := subscription.ReceiveMessage(waitCtx); err != nil {
        if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
            return v.Generation(ctx)
        }
        return 0, err
    }
    generation, err = v.Generation(ctx)
    if err != nil || generation != after {
        return generation, err
    }
}
```

Reject nil receivers, negative `after`, and non-positive waits. Wrap errors without Redis address, credentials, or message data. Set `Revocations: verifications` in `NewServiceContext`.

- [ ] **Step 7: Verify GREEN and commit**

```bash
gofmt -w service/aiServeWeaveControlPlane/internal/handler/revocations.go service/aiServeWeaveControlPlane/internal/handler/revocations_test.go service/aiServeWeaveControlPlane/e2e/revocations_test.go service/aiServeWeaveControlPlane/internal/handler/routes.go service/aiServeWeaveControlPlane/internal/cache/cache.go service/aiServeWeaveControlPlane/internal/cache/cache_live_test.go service/aiServeWeaveControlPlane/internal/svc/servicecontext.go service/aiServeWeaveControlPlane/internal/types/types.go
go test ./service/aiServeWeaveControlPlane/internal/handler -count=1
go test ./service/aiServeWeaveControlPlane/e2e -run TestRevocationWatchRouteAndGuard -count=1
AISW_REDIS_TEST_ADDR=127.0.0.1:6379 go test -race ./service/aiServeWeaveControlPlane/internal/cache -count=1
git add service/aiServeWeaveControlPlane/internal/handler/revocations.go service/aiServeWeaveControlPlane/internal/handler/revocations_test.go service/aiServeWeaveControlPlane/e2e/revocations_test.go
git add -p service/aiServeWeaveControlPlane/internal/handler/routes.go service/aiServeWeaveControlPlane/internal/cache/cache.go service/aiServeWeaveControlPlane/internal/cache/cache_live_test.go service/aiServeWeaveControlPlane/internal/svc/servicecontext.go service/aiServeWeaveControlPlane/internal/types/types.go
git diff --cached --check
git commit -m "feat: publish API key revocation generations"
```

---

### Task 2: Fail-closed Gateway watcher and epoch fence

**Files:**
- Create: `service/aiServeWeaveGateway/controlplaneclient/revocations.go`
- Create: `service/aiServeWeaveGateway/controlplaneclient/revocations_test.go`
- Modify: `service/aiServeWeaveGateway/controlplaneclient/verifier.go`
- Modify: `service/aiServeWeaveGateway/controlplaneclient/verifier_test.go`

**Interfaces:**
- Consumes: `GET /internal/v1/apikeys/revocations/watch?after=N` and existing `runtime.Clock`.
- Produces: `(*Verifier).RunRevocationWatch(context.Context)` and cache writes conditional on healthy notification state plus unchanged local epoch.

Keep `revocations_test.go` in package `controlplaneclient_test`, matching `verifier_test.go`, so both files share the existing Key generator and fake-clock helpers while testing only exported behavior.

- [ ] **Step 1: Write failing state-machine tests**

Use a path-aware `httptest.Server` with channels for watch request, scripted response, blocked verification, and cancellation. The essential dispatcher is:

```go
func (cp *revocationControlPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    switch r.URL.Path {
    case "/internal/v1/apikeys/verify":
        cp.serveVerification(w, r)
    case "/internal/v1/apikeys/revocations/watch":
        cp.serveWatch(w, r)
    default:
        http.NotFound(w, r)
    }
}

type watchAnswer struct {
    status     int
    body       string
    generation int64
}
```

`serveWatch` sends the parsed `after` value to a buffered `watchRequests` channel, then selects between `r.Context().Done()` and the test-controlled `watchAnswers` channel. `serveVerification` increments `verifyCalls`, optionally waits on the one-shot block channel, and returns either the existing valid identity or 404. Add these observable behaviors:

```go
func TestCacheRequiresHealthyRevocationWatch(t *testing.T) {
    cp := newRevocationControlPlane(t)
    verifier := newRevocationVerifier(t, cp)
    key := mustKey(t)
    mustVerify(t, verifier, key)
    mustVerify(t, verifier, key)
    if got := cp.verifyCalls.Load(); got != 2 {
        t.Fatalf("verify calls before sync = %d, want 2", got)
    }
    cancel, done := runWatcher(t, verifier)
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

func TestGenerationChangeClearsCache(t *testing.T) {
    cp, verifier, cancel, done := runningHealthyVerifier(t, 0)
    defer func() { cancel(); <-done }()
    key := mustKey(t)
    mustVerify(t, verifier, key)
    mustVerify(t, verifier, key)
    cp.answerWatch(1)
    cp.waitForWatchAfter(t, 1)
    mustVerify(t, verifier, key)
    if got := cp.verifyCalls.Load(); got != 2 {
        t.Errorf("verify calls after invalidation = %d, want 2", got)
    }
}

func TestWatchFailureBlocksStaleRefill(t *testing.T) {
    cp, verifier, cancel, done := runningHealthyVerifier(t, 0)
    defer func() { cancel(); <-done }()
    key := mustKey(t)
    release := cp.blockNextVerification()
    firstDone := make(chan error, 1)
    go func() {
        _, err := verifier.Verify(context.Background(), key)
        firstDone <- err
    }()
    cp.waitForBlockedVerification(t)
    cp.failWatch(http.StatusServiceUnavailable)
    cp.waitForRetryTimer(t)
    release()
    if err := <-firstDone; err != nil {
        t.Fatalf("in-flight Verify error = %v, want nil", err)
    }
    mustVerify(t, verifier, key)
    if got := cp.verifyCalls.Load(); got != 2 {
        t.Errorf("verify calls after stale response = %d, want 2", got)
    }
}
```

Table-test lower generation reset, negative generation, malformed JSON, >1 KiB response, `401`, `404`, `500`, transport error, and cancellation. Each failure case proves two subsequent verifications make two ControlPlane calls.

Add a silent-response case with `RevocationTimeout: 25*time.Millisecond`. The fake returns the bootstrap response, then blocks on the next request context. Wait for the injected clock's first retry timer as proof that the HTTP timeout fired, then assert two verifications make two upstream calls. This exercises timeout-driven cache disable without a sleep.

- [ ] **Step 2: Verify RED**

```bash
go test ./service/aiServeWeaveGateway/controlplaneclient -run 'Test(CacheRequiresHealthyRevocationWatch|GenerationChangeClearsCache|WatchFailureBlocksStaleRefill)' -count=1
```

Expected: compile failure because watcher and fail-closed state do not exist.

- [ ] **Step 3: Add health, generation, and epoch to the verifier**

Extend `Config` with `RevocationHTTPClient`, `RevocationTimeout`, `RetryMin`, `RetryMax`, and `Logger *slog.Logger`. Extend `Verifier` with `generation int64`, `cacheHealthy bool`, `epoch uint64`, the dedicated watch client, retry bounds, and logger.

Change `cached` to return `(Identity, bool, uint64)`. It snapshots epoch under the mutex and returns a miss whenever `cacheHealthy` is false. Change `store` to accept that epoch and return without writing unless `cacheHealthy && v.epoch == epoch`. Keep existing TTL and bounded eviction unchanged.

```go
func (v *Verifier) cached(hash string) (httpapi.Identity, bool, uint64) {
    now := v.clock.Now()
    v.mu.Lock()
    defer v.mu.Unlock()
    epoch := v.epoch
    if !v.cacheHealthy {
        return httpapi.Identity{}, false, epoch
    }
    found, ok := v.entries[hash]
    if !ok {
        return httpapi.Identity{}, false, epoch
    }
    if !now.Before(found.expiry) {
        delete(v.entries, hash)
        return httpapi.Identity{}, false, epoch
    }
    return found.identity, true, epoch
}
```

In `store(hash, identity, epoch)`, acquire the same mutex and return before the existing capacity/eviction block when health is false or the epoch differs. In `Verify`, use `identity, ok, epoch := v.cached(hash)` and pass `epoch` to `store` after `ask` succeeds.

- [ ] **Step 4: Implement bounded watch parsing and transitions**

Create `revocations.go` with:

```go
const (
    DefaultRevocationWatchTimeout = 5 * time.Second
    DefaultRevocationRetryMin     = 100 * time.Millisecond
    DefaultRevocationRetryMax     = 2 * time.Second
    maxRevocationResponseBytes    = 1 << 10
)
```

`watchGeneration` sends only `after` plus the existing InternalToken, refuses redirects, accepts only 200, reads at most 1,025 bytes, and unmarshals one non-negative generation. Never include the response body in an error.

Implement:

```go
func (v *Verifier) applyGeneration(generation int64) (recovered, reset bool) {
    v.mu.Lock()
    defer v.mu.Unlock()
    recovered = !v.cacheHealthy
    reset = v.cacheHealthy && generation < v.generation
    if recovered || generation != v.generation {
        v.epoch++
        clear(v.entries)
    }
    v.generation = generation
    v.cacheHealthy = true
    return recovered, reset
}

func (v *Verifier) disableCache() bool {
    v.mu.Lock()
    defer v.mu.Unlock()
    changed := v.cacheHealthy
    if v.cacheHealthy || len(v.entries) > 0 {
        v.epoch++
        clear(v.entries)
    }
    v.cacheHealthy = false
    return changed
}
```

`RunRevocationWatch` disables cache before retry, doubles backoff with an overflow-safe 2-second cap, resets after success, and uses `Clock.NewTimer` so tests advance it explicitly. Log only healthy→unhealthy, unhealthy→healthy, and lower-generation reset; never log token, cursor, hash, headers, or body.

- [ ] **Step 5: Adapt existing tests to initial synchronization**

Make the existing fake route verification and watch paths separately. Start the watcher in the cache-test helper, answer the initial generation zero, wait until the next watch proves it was applied, and register cancel-plus-join in `t.Cleanup`. Preserve every existing literal verification-call expectation.

```go
watchCtx, cancel := context.WithCancel(context.Background())
done := make(chan struct{})
go func() {
    defer close(done)
    verifier.RunRevocationWatch(watchCtx)
}()
cp.answerWatch(0)
cp.waitForWatchAfter(t, 0)
t.Cleanup(func() {
    cancel()
    <-done
})
```

- [ ] **Step 6: Verify GREEN and commit**

```bash
gofmt -w service/aiServeWeaveGateway/controlplaneclient/verifier.go service/aiServeWeaveGateway/controlplaneclient/verifier_test.go service/aiServeWeaveGateway/controlplaneclient/revocations.go service/aiServeWeaveGateway/controlplaneclient/revocations_test.go
go test -race ./service/aiServeWeaveGateway/controlplaneclient -count=1
git add service/aiServeWeaveGateway/controlplaneclient/revocations.go service/aiServeWeaveGateway/controlplaneclient/revocations_test.go
git add -p service/aiServeWeaveGateway/controlplaneclient/verifier.go service/aiServeWeaveGateway/controlplaneclient/verifier_test.go
git diff --cached --check
git commit -m "feat: invalidate Gateway API key caches"
```

---

### Task 3: Own watcher lifecycle and prove multi-Gateway catch-up

**Files:**
- Modify: `service/aiServeWeaveGateway/main.go`
- Modify: `service/aiServeWeaveGateway/controlplaneclient/revocations_test.go`

**Interfaces:**
- Consumes: concrete `*controlplaneclient.Verifier` and `RunRevocationWatch`.
- Produces: exactly one watcher per ControlPlane-configured Gateway, canceled and joined on every `run` exit path.

- [ ] **Step 1: Write the failing two-verifier test**

Use one fake ControlPlane and two verifier instances. Advance its authoritative generation without depending on delivery, wait through channels until both watchers request `after=1`, then make verification reject:

```go
func TestEveryVerifierInvalidatesAndReconnectCatchesUp(t *testing.T) {
    cp := newRevocationControlPlane(t)
    first := newRevocationVerifier(t, cp)
    second := newRevocationVerifier(t, cp)
    stopFirst, firstDone := runWatcher(t, first)
    stopSecond, secondDone := runWatcher(t, second)
    defer func() {
        stopFirst()
        stopSecond()
        <-firstDone
        <-secondDone
    }()
    cp.bootstrapWatchers(t, 2, 0)
    key := mustKey(t)
    mustVerify(t, first, key)
    mustVerify(t, second, key)
    cp.advanceWithoutDelivery(1)
    deadline, cancelDeadline := context.WithTimeout(context.Background(), time.Second)
    defer cancelDeadline()
    cp.waitForWatchersAfter(deadline, t, 2, 1)
    cp.rejectVerifications.Store(true)
    for i, verifier := range []*controlplaneclient.Verifier{first, second} {
        if _, err := verifier.Verify(context.Background(), key); !errors.Is(err, httpapi.ErrKeyRejected) {
            t.Errorf("verifier %d error = %v, want ErrKeyRejected", i, err)
        }
    }
}
```

This test names the missed-Pub/Sub break: deleting the current-generation comparison would leave both cached identities accepted.

The one-second context is the healthy-path acceptance bound. `waitForWatchersAfter` must select on that context and the fake's watch-request channel; it must not poll or sleep.

- [ ] **Step 2: Verify RED**

```bash
go test ./service/aiServeWeaveGateway/controlplaneclient -run TestEveryVerifierInvalidatesAndReconnectCatchesUp -count=1
```

Expected: failure until both independent loops apply the cursor jump.

- [ ] **Step 3: Wire the concrete verifier in `main.go`**

Change `keyVerifier` to return `*controlplaneclient.Verifier` and pass `Logger: logger`. After construction:

```go
if verifier != nil {
    watchCtx, stopWatch := context.WithCancel(ctx)
    watchDone := make(chan struct{})
    go func() {
        defer close(watchDone)
        verifier.RunRevocationWatch(watchCtx)
    }()
    defer func() {
        stopWatch()
        <-watchDone
    }()
}
```

Pass the pointer unchanged to `httpapi.Config.Verifier`. Nil static/no-auth mode starts no watcher.

- [ ] **Step 4: Verify lifecycle, race safety, and linkage**

```bash
gofmt -w service/aiServeWeaveGateway/main.go service/aiServeWeaveGateway/controlplaneclient/revocations_test.go
go test -race ./service/aiServeWeaveGateway/controlplaneclient -count=1
go build ./...
```

Expected: PASS, all service binaries link, and package `TestMain` detects no watcher leak.

- [ ] **Step 5: Commit Task 3**

```bash
git add -p service/aiServeWeaveGateway/main.go service/aiServeWeaveGateway/controlplaneclient/revocations_test.go
git diff --cached --check
git commit -m "feat: run Gateway revocation watchers"
```

---

### Task 4: Document, verify, and close P06

**Files:**
- Modify: `service/aiServeWeaveGateway/README.md`
- Modify: `service/aiServeWeaveControlPlane/README.md`
- Modify: `service/aiServeWeaveControlPlane/etc/controlplane.yaml`
- Modify: `deploy/controlplane.yaml`
- Modify: `README.md`
- Modify: `CHANGELOG.md`
- Modify: `STATUS.md`

**Interfaces:**
- Consumes: verified Tasks 1–3.
- Produces: exact rollout/failure documentation and evidence-backed P06 completion.

- [ ] **Step 1: Update docs without overstating the SLA**

Document this semantic content:

```text
Healthy Gateway replicas clear positive verifications within one second of a recorded generation change. A watch error clears and disables local caching; verification then calls ControlPlane per request and returns 503 when unavailable. Silent watch failure is detected within five seconds. Missed Pub/Sub and replica switches catch up from Redis generation. A ControlPlane crash after database commit but before Redis invalidation remains bounded by -key-cache-ttl (30s default) until P07 adds a relational outbox.
```

Also document the endpoint, InternalToken reuse, two-second heartbeat, ControlPlane-before-Gateway rollout, safe mixed-version behavior, and non-interruption of already-authenticated inference. Correct both YAML TTL comments.

- [ ] **Step 2: Run focused real-service checks**

```bash
AISW_REDIS_TEST_ADDR=127.0.0.1:6379 go test -race ./service/aiServeWeaveControlPlane/internal/cache -count=1
go test -race ./service/aiServeWeaveControlPlane/internal/handler ./service/aiServeWeaveGateway/controlplaneclient -count=1
```

Expected: PASS. Retain the exact commands for `STATUS.md` evidence.

- [ ] **Step 3: Run every Go quality gate**

```bash
gofmt -l ./service ./api
go vet ./...
go build ./...
go generate ./api/...
git diff --exit-code -- api
go test ./...
go test -race ./service/...
```

Expected: no `gofmt` output and zero exit from every other command. Diagnose any generated-code diff; do not stage it automatically.

- [ ] **Step 4: Audit secrets, bounds, and diff scope**

```bash
rg -n "Authorization|APIKey|Hash|Prompt|Workflow" service/aiServeWeaveControlPlane/internal/cache service/aiServeWeaveControlPlane/internal/handler/revocations.go service/aiServeWeaveGateway/controlplaneclient/revocations.go
git diff --check
git diff --stat
```

Expected: authorization appears only where the Gateway sets InternalToken; notifications/logs contain generation only; the response read is bounded; no message queue is unbounded.

- [ ] **Step 5: Mark P06 complete only after evidence**

Change P06 to `[x]` and record the generation cursor, fail-closed cache, ≤1-second healthy propagation, ≤5-second silent detection, reconnect compensation, exact tests, and accepted 30-second crash-only backstop. Keep P07 outbox work unchecked.

- [ ] **Step 6: Commit docs and inspect final scope**

```bash
git add -p STATUS.md README.md CHANGELOG.md service/aiServeWeaveGateway/README.md service/aiServeWeaveControlPlane/README.md service/aiServeWeaveControlPlane/etc/controlplane.yaml deploy/controlplane.yaml
git diff --cached --check
git commit -m "docs: complete P06 revocation notifications"
git status --short
git log -5 --oneline
```

Expected: the four P06 commits contain only planned behavior. Existing P05 work may remain unstaged, but no unrelated hunk is included.
