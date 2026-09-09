# P06 Key Revocation Notification Design

## Objective

P06 shortens the Gateway-local API Key revocation window by pushing ControlPlane verification-generation changes to every Gateway replica. A healthy replica drops stale positive verifications within one second of a successful revocation. A replica that cannot prove the notification path is healthy immediately stops using and populating its positive cache, so subsequent requests go to the authoritative ControlPlane verification endpoint.

The implementation covers the ControlPlane and Gateway. It does not change the Console, the tunnel protocol, the relational schema, or the API Key format.

## Approved security and availability rules

- A Gateway starts with its positive verification cache disabled. It enables caching only after reading an authoritative generation from the ControlPlane notification endpoint.
- A transport error, five-second client timeout, non-2xx response, authentication failure, malformed response, or negative generation synchronously clears the Gateway cache and keeps caching disabled.
- While notification health is unknown or unhealthy, every API Key request is verified against the existing ControlPlane endpoint. A rejected Key remains `401`; an unavailable ControlPlane remains `503`. Stale cached credentials are never used as an availability fallback.
- A healthy notification advances or resets the local generation, clears all cached positive verifications, and permits caching again. Resetting to a lower non-negative generation is treated as a Redis epoch reset: the cache is cleared before the lower value becomes the new baseline.
- Revocation does not interrupt an inference request that already passed authentication. The enforcement boundary is the next authentication attempt after a Gateway applies the new generation.
- `-key-cache-ttl` remains a defense-in-depth expiry and the bound for the explicitly accepted ControlPlane crash window described below. It is no longer the ordinary connected-path revocation delay.

## Architecture

The existing Redis verification generation becomes both the ControlPlane cache namespace and the cross-replica notification cursor. No Key, Key hash, tenant id, or user id appears in a notification.

The ControlPlane exposes this InternalToken-protected endpoint:

```text
GET /internal/v1/apikeys/revocations/watch?after=<non-negative-int64>
Authorization: Bearer <InternalToken>

200 OK
Content-Type: application/json

{"generation":42}
```

The handler waits for at most two seconds. It returns immediately when the current Redis generation differs from `after`; otherwise it returns the unchanged generation as a heartbeat when the wait expires. Clients cannot supply a custom wait duration, so one authenticated request cannot create an unbounded wait.

Each request subscribes to the Redis notification channel and confirms the subscription before it reads the current generation. This ordering closes the read-then-subscribe race: a change before the read is visible in the generation, and a change after the read is delivered by Pub/Sub. Redis Pub/Sub is only a wake-up optimization. The persisted generation is the source for missed-event and reconnect compensation.

API Key revocation and user-disable invalidation use one Redis Lua script to increment the generation and publish the resulting value atomically. The message contains only that integer. Clearing the complete positive cache is intentional: it is constant-time, matches the existing generation-wide ControlPlane cache invalidation, and handles a user disable that revokes an arbitrary number of Keys without building an unbounded message.

## Gateway state machine

`controlplaneclient.Verifier` owns the local cache and notification state:

```text
startup
  -> unhealthy, cache empty
  -> successful initial watch response
  -> healthy(generation), caching enabled

healthy(generation)
  -> same generation heartbeat: remain healthy
  -> different non-negative generation: clear, adopt generation, remain healthy
  -> watch failure: clear, enter unhealthy

unhealthy
  -> verification request: bypass cache and do not populate it
  -> successful watch response: clear, adopt generation, enter healthy
```

The notification loop uses a dedicated HTTP client with a five-second timeout rather than the verification client's three-second request-path timeout. After a failure it retries with injected-clock exponential backoff starting at 100 milliseconds and capped at two seconds. The cache remains disabled throughout the backoff. Logging occurs only on health transitions, not on every failed retry.

The Gateway main process starts the loop whenever `-control-plane-addr` configures a ControlPlane verifier. Shutdown cancels the loop and waits for its goroutine before returning. Static `-api-keys` mode and unauthenticated local mode do not start a notification loop and retain their current behavior.

## Concurrency correctness

Clearing the map alone is insufficient. A verification can miss the local cache, receive an active result from the ControlPlane, overlap a revocation notification, and return after the notification cleared the map. Storing that old result would recreate the stale entry.

Every local cache state change therefore advances a process-local epoch. `Verify` snapshots the epoch before its ControlPlane call. It stores a successful result only when all three conditions still hold under the cache mutex:

1. notification health is still healthy;
2. the local epoch still equals the snapshot; and
3. the cache remains within its configured capacity rule.

A notification or health failure during the call changes the epoch, so the old result can satisfy the in-flight request but cannot repopulate the cache. The next request goes through current authentication state.

The current mutex continues to protect the entry map, health state, generation, and epoch. The notification loop never sends through an unbounded channel; it mutates the verifier directly after a bounded HTTP response.

## Failure and recovery semantics

### Missed Pub/Sub message

The next heartbeat or reconnect reads the persisted generation. A value different from the Gateway cursor clears the cache, so message loss cannot extend stale acceptance to the cache TTL.

### Gateway-to-ControlPlane disconnection

An explicit transport failure disables the cache immediately. A silent half-open connection is detected by the dedicated five-second HTTP timeout; cache use can therefore continue for at most five seconds after an otherwise invisible break. After detection, verification is synchronous and fail-closed until notification health recovers.

### Gateway restart

The new process starts with an empty disabled cache, reads the current generation, and only then enables caching. It cannot inherit a stale in-memory verification.

### ControlPlane replica restart or switch

The replacement replica reads the shared Redis generation. The Gateway supplies its last cursor on every watch, so switching replicas needs no sticky session and loses no recorded invalidation.

### Redis restart or restored older generation

A lower non-negative generation is an authoritative epoch reset. The Gateway clears its cache before adopting it. If Redis is unavailable, the watch endpoint returns `503`, which disables Gateway caching. Redis persistence requirements from P05 remain unchanged.

### Database commit followed by ControlPlane crash before Redis invalidation

The relational database and Redis do not share a transaction. If the ControlPlane process crashes after the database commits a revocation but before the atomic Redis increment-and-publish script runs, no new generation exists for notification or reconnect logic to observe. This explicitly accepted narrow window falls back to `-key-cache-ttl`, 30 seconds by default.

Eliminating that final window requires a durable relational outbox or revocation version advanced inside the same database transaction. P06 does not add a schema or couple itself to the current `AutoMigrate`; P07 will own that versioned migration and outbox handoff. P06 documentation and status evidence must not claim a universal zero-second revocation guarantee.

## Bounds and timing contract

- The ControlPlane long-poll wait is fixed at two seconds.
- The Gateway watch request timeout is fixed at five seconds.
- Retry backoff starts at 100 milliseconds and is capped at two seconds.
- A healthy reachable ControlPlane and Redis must invalidate every connected test Gateway within one second of a successful revocation.
- An otherwise silent notification-path failure must disable cache use within five seconds.
- Once a failure is observed, clearing and disabling happen synchronously before retry backoff begins.
- The commit-before-invalidation crash window remains bounded by the configured `-key-cache-ttl`, 30 seconds by default.

These values are constants rather than new flags. They define one security contract, keep configuration surface small, and can be changed later only with corresponding tests and documentation.

## Components and interfaces

### ControlPlane cache

`internal/cache.Verifications` gains operations to read the current generation and wait for a generation change. Its invalidation methods retain the `logic.Invalidator` boundary and remain best-effort after a committed database mutation; the new atomic Lua script prevents an increment from succeeding without its matching publish on Redis.

The watch operation accepts a context, the caller's generation, and the server-owned two-second duration. Redis errors are returned to the handler rather than collapsed into a cache miss because notification health is a security decision, not a performance optimization.

### ControlPlane handler

The handler validates exactly one `after` query parameter as a non-negative base-10 `int64`, calls the watch operation, and returns a bounded JSON response. Missing, repeated, malformed, negative, or overflowing values return `400`. Redis failures return `503`. The route uses the existing constant-time shared-secret middleware and a route timeout longer than the two-second wait.

### Gateway verifier and watcher

The verifier exposes a cancellable `RunRevocationWatch(context.Context)` loop on the concrete type. Its configuration permits injected HTTP clients, clocks, wait durations, and retry bounds for deterministic package tests; production uses the fixed defaults above. `Verify` still sends only the SHA-256 hash to the existing verification endpoint.

Main retains the concrete verifier long enough to start and stop the watcher, then passes it to `httpapi.Config` through the existing `httpapi.KeyVerifier` interface. No HTTP API or scheduler type needs to know notification details.

## Testing and acceptance

### Default automated tests

- ControlPlane handler tests cover the InternalToken guard, exact query validation, unchanged heartbeat, changed generation, Redis/source failure as `503`, and bounded JSON.
- Gateway verifier tests prove the cache is disabled before initial synchronization, enabled after it, cleared by a higher generation, cleared safely by a lower generation, and disabled after every watch failure class.
- A deterministic blocked-response test proves a notification that overlaps an in-flight verification prevents that result from repopulating the cache.
- Reconnect tests prove a cursor gap clears the cache even when no Pub/Sub message reaches the Gateway.
- Multi-Gateway tests prove one generation change wakes and invalidates every independent verifier.
- Cancellation tests prove the watch loop exits without leaking a goroutine.
- Existing plaintext-Key tests continue to prove only hashes cross the verification endpoint; watch request and response capture proves notifications contain no Key material.

Tests use injected clocks, channels, and request contexts for sequencing. They do not use `time.Sleep` to advance time, and failures include both actual and expected values.

### Real Redis verification

With `AISW_REDIS_TEST_ADDR` set, cache integration tests verify:

- increment and publish are observed as one invalidation;
- two concurrent subscribers observe the same generation;
- a subscriber that intentionally misses Pub/Sub catches up from the current generation;
- disconnecting Redis makes an outstanding watch fail; and
- reconnecting reads the persisted generation.

The test namespace is isolated and cleanup removes only keys created by the test. It never flushes the Redis database.

### End-to-end timing verification

Run a real ControlPlane with Redis and two Gateway verifier instances. Prime both local caches, revoke the Key through the tenant API, and assert both reject it within one second. Then interrupt only the watch path, keep verification reachable, and assert both stop serving from cache within five seconds. Timing tests use deadlines as failure bounds, not sleeps as state progression.

Run the repository quality gates:

```bash
gofmt -l ./service ./api
go vet ./...
go build ./...
go generate ./api/...
git diff --exit-code -- api
go test ./...
go test -race ./service/...
```

## Documentation and rollout

Update `STATUS.md`, the root README code map if package responsibilities change, both service READMEs, sample ControlPlane configuration comments, and deployment documentation. The deployed API requires no new secret: the watch endpoint reuses `InternalToken` and the Gateway reuses `AISW_CONTROL_PLANE_TOKEN`.

Roll out the ControlPlane first so the watch endpoint exists, then Gateway replicas. An old Gateway continues using the documented TTL behavior. A new Gateway talking to an old ControlPlane keeps its cache disabled because the watch endpoint is unavailable; verification remains functional but pays one ControlPlane call per request. This fail-safe mixed-version behavior is deliberate.

## Out of scope

- A relational outbox that closes the database-commit-to-Redis-invalidation crash window; P07 owns its schema and migration.
- Interrupting already-authenticated inference calls.
- Per-Key notification payloads, per-tenant notification streams, negative verification caching, or a durable event history.
- New API Key scopes, formats, hashing, storage, or Console behavior.
- mTLS for Gateway-to-ControlPlane internal HTTP, Redis high availability, metrics, tracing, and alerting.
