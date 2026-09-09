# P05 User and Session Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Redis-backed revocable sessions and complete tenant-user and platform-operator lifecycle management in the ControlPlane and Console.

**Architecture:** Signed JWTs identify a bounded Redis session; every protected request validates both. Lifecycle mutations close a Redis login gate and revoke sessions before performing transactionally protected database changes, so races fail closed. Tenant and platform management remain separate HTTP and Console surfaces.

**Tech Stack:** Go 1.27, go-zero REST, gorm, go-redis v9, PostgreSQL/MySQL, Next.js 16, React 19, TypeScript 6/7, Node test runner, Redis 8 AOF.

**Spec:** `docs/superpowers/specs/2026-09-09-p05-user-session-lifecycle-design.md`

## Global Constraints

- Redis is required; session-dependent operations fail closed and distinguish unavailable Redis (`503`) from invalid sessions (`401`).
- Redis stores no signed JWT, password, API Key, prompt, workflow JSON, or authorization header.
- Each subject has at most 20 sessions; all Redis reads and Lua loops are bounded.
- All new or changed Go comments are English/Chinese pairs; exported identifiers have bilingual doc comments beginning with the identifier.
- Default tests use injected clocks and no external database, Redis, Gateway, GPU, network, or real sleeps.
- Tenant and platform identities, cookies, routes, and guards remain mutually isolated.
- P06 Gateway push invalidation, MFA, account deletion, reset email, and device-level session inventory are out of scope.
- The starting worktree contains pre-existing P02–P04 changes. Task checkpoints inspect only P05 paths/hunks and do not stage or commit mixed files; commits are deferred unless a P05 change is provably isolated from the user's existing work.

---

### Task 1: Revocable session token and store contracts

**Files:**
- Modify: `service/aiServeWeaveControlPlane/internal/token/token.go`
- Modify: `service/aiServeWeaveControlPlane/internal/token/token_test.go`
- Create: `service/aiServeWeaveControlPlane/internal/session/session.go`
- Create: `service/aiServeWeaveControlPlane/internal/session/memory.go`
- Create: `service/aiServeWeaveControlPlane/internal/session/redis.go`
- Create: `service/aiServeWeaveControlPlane/internal/session/session_test.go`
- Create: `service/aiServeWeaveControlPlane/internal/session/redis_live_test.go`

**Interfaces:**
- Produces: `token.Claims{SessionID, UserID, TenantID, Role string}`; `Issuer.Issue(token.Claims)` requires a non-empty session id; `Issuer.Parse` rejects tokens without it.
- Produces: `session.Subject{Kind, ID string}`, `session.Record{ID string; Subject Subject; TenantID, Role string; ExpiresAt time.Time}`.
- Produces: `session.Store` with `Create`, `Validate`, `Revoke`, `RevokeAll`, `BeginMutation`, and `EndMutation`; `session.ErrInvalid`, `session.ErrUnavailable`, and `session.ErrMutationActive`.
- Produces: `session.NewRedis(*redis.Client, runtime.Clock)` and `session.NewMemory(runtime.Clock)`.

- [ ] **Step 1: Write failing token tests**

Add a literal session id to the issue/parse round trip and construct a correctly signed legacy JWT without `sid`:

```go
func TestParseRejectsTokenWithoutSessionID(t *testing.T) {
	issuer := newTestIssuer(t)
	legacy := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "usr_1", "tenant_id": "tnt_1", "role": "owner",
		"iat": testNow.Unix(), "exp": testNow.Add(time.Hour).Unix(),
	})
	signed, err := legacy.SignedString([]byte(testSecret))
	if err != nil { t.Fatalf("SignedString: %v", err) }
	if _, err := issuer.Parse(signed); !errors.Is(err, token.ErrInvalid) {
		t.Errorf("Parse error = %v, want %v", err, token.ErrInvalid)
	}
}
```

- [ ] **Step 2: Run the token test and verify RED**

Run: `go test ./service/aiServeWeaveControlPlane/internal/token -run 'TestParseRejectsTokenWithoutSessionID|TestIssuedTokenParsesBack'`

Expected: FAIL because `Claims`/JWT do not yet carry or require `SessionID`.

- [ ] **Step 3: Add the `sid` claim minimally**

Extend the existing claim constants, `Claims`, `Issue`, and `Parse`; make both issue and parse return `ErrInvalid` for an empty session id rather than emitting an unrevocable token.

```go
type Claims struct {
	SessionID string
	UserID    string
	TenantID  string
	Role      string
}
```

- [ ] **Step 4: Run token tests and verify GREEN**

Run: `go test ./service/aiServeWeaveControlPlane/internal/token`

Expected: PASS.

- [ ] **Step 5: Write failing table-driven session contract tests**

Cover exact claim matching, expiry via the fake clock, 20-session eviction, current-session revoke, idempotent bulk revoke, gate refusal, nonce ownership, and backend failure:

```go
func TestStoreContract(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, session.Store, *runtimetest.Clock)
	}{
		{name: "the twenty-first session evicts the earliest expiry", run: testBoundedSessions},
		{name: "a mutation gate refuses session creation", run: testMutationGate},
		{name: "bulk revocation is idempotent", run: testBulkRevoke},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, session.NewMemory(clock), clock) })
	}
}
```

Before writing each body, identify the production mutation it catches: accepting mismatched claims, retaining session 21, allowing a login through a gate, or treating a repeated revoke as a change.

- [ ] **Step 6: Run session tests and verify RED**

Run: `go test ./service/aiServeWeaveControlPlane/internal/session`

Expected: FAIL because the package and contracts do not exist.

- [ ] **Step 7: Implement the bounded in-memory and Redis stores**

Use versioned keys from the spec. Redis Lua scripts must perform create/prune/cap, single revoke, bulk revoke, gate begin, and nonce-checked gate end atomically. `Validate` compares the full stored record to the caller's expected record. Map Redis transport/script failures to `ErrUnavailable`, absence/mismatch/expiry to `ErrInvalid`, and an active gate to `ErrMutationActive`.

- [ ] **Step 8: Verify the default and optional real-Redis contracts**

Run: `go test ./service/aiServeWeaveControlPlane/internal/session`

Run when `AISW_REDIS_TEST_ADDR` is set: `go test ./service/aiServeWeaveControlPlane/internal/session -run TestLiveRedis -count=1`

Expected: PASS; the live test also proves a session and a subsequent revocation survive Redis restart when the test harness provides a restartable AOF instance.

- [ ] **Step 9: Commit Task 1**

```bash
git add service/aiServeWeaveControlPlane/internal/token service/aiServeWeaveControlPlane/internal/session
git commit -m "feat(controlplane): add Redis-backed revocable sessions"
```

### Task 2: Require Redis sessions on login and protected requests

**Files:**
- Modify: `service/aiServeWeaveControlPlane/internal/config/config.go`
- Create: `service/aiServeWeaveControlPlane/internal/config/config_test.go`
- Modify: `service/aiServeWeaveControlPlane/internal/cache/cache.go`
- Modify: `service/aiServeWeaveControlPlane/internal/svc/servicecontext.go`
- Modify: `service/aiServeWeaveControlPlane/internal/handler/middleware.go`
- Modify: `service/aiServeWeaveControlPlane/internal/handler/handlers.go`
- Modify: `service/aiServeWeaveControlPlane/internal/handler/routes.go`
- Modify: `service/aiServeWeaveControlPlane/internal/types/types.go`
- Modify: `service/aiServeWeaveControlPlane/e2e/harness_test.go`
- Create: `service/aiServeWeaveControlPlane/e2e/sessions_test.go`

**Interfaces:**
- Consumes: Task 1 `session.Store` and session-aware token claims.
- Produces: `ServiceContext.Sessions session.Store` and a shared Redis client owned by `ServiceContext`.
- Produces: `logic.WithSessions`, `Service.Logout`, and `Service.RevokeOwnSessions`, so successful revocations use the closed audit vocabulary instead of bypassing the business layer.
- Produces: `DELETE /admin/v1/auth/session`, `POST /admin/v1/auth/sessions/revoke`, `DELETE /operator/v1/auth/session`, and `POST /operator/v1/auth/sessions/revoke`.
- Produces: middleware actors with `SessionID`, enabling current-session logout without reading a credential from the request body.

- [ ] **Step 1: Write failing config and HTTP tests**

Add table cases showing an empty Redis address fails validation, a legacy JWT gets `401`, missing session gets `401`, Redis failure gets `503`, and tenant/platform guards still reject the opposite surface.

```go
func TestProtectedRouteRequiresLiveSession(t *testing.T) {
	h := newHarness(t)
	token := h.loginOwner()
	h.sessions.RevokeAll(context.Background(), tenantSubject(h.ownerID))
	if got := h.call(http.MethodGet, "/admin/v1/users", token, nil, nil); got != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", got, http.StatusUnauthorized)
	}
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./service/aiServeWeaveControlPlane/internal/config ./service/aiServeWeaveControlPlane/e2e -run 'Redis|Session|Legacy|Guard'`

Expected: FAIL because Redis is optional and middleware validates JWT only.

- [ ] **Step 3: Share one Redis client and enforce configuration**

Construct one `redis.Client` in `NewServiceContext`, ping it, pass it to both the API Key verification cache and Redis session store, and close it once in `ServiceContext.Close`. Refactor `cache.Verifications` to accept the client without changing its positive-only/fallback behavior. `Config.Validate` must reject `Redis.Addr == ""` with a fixed non-secret error.

- [ ] **Step 4: Create sessions during login**

After password authentication, mint a random session id, issue the JWT, then create the matching Redis record before returning the token. If Redis refuses due to a mutation gate or is unavailable, never return the signed token.

- [ ] **Step 5: Validate Redis in both middleware guards**

Parse JWT first, preserve tenant/platform scope checks, validate the exact session record, and map only `session.ErrUnavailable` to `503`. Attach `SessionID` to `logic.Actor` only after all checks pass.

- [ ] **Step 6: Implement audited current and all-session revocation**

Add the session dependency to `logic.Service`. The current logout method revokes `Actor.SessionID` and records `session.logout`/`platform_session.logout` only when it removed a live session. The self bulk method revokes by subject and records the applicable `*.sessions.revoke` action only when at least one session changed. Handlers accept an empty JSON object under the existing body bound and return `204`; repeating bulk revoke returns `204` without a duplicate audit row.

- [ ] **Step 7: Update the e2e harness with the in-memory session store**

Keep default tests external-service-free by injecting `session.NewMemory(clock)` into `ServiceContext`; expose it on the harness only for behavior setup, not assertions about fake calls.

- [ ] **Step 8: Run focused and package tests**

Run: `go test ./service/aiServeWeaveControlPlane/internal/config ./service/aiServeWeaveControlPlane/internal/cache ./service/aiServeWeaveControlPlane/internal/handler ./service/aiServeWeaveControlPlane/e2e`

Expected: PASS.

- [ ] **Step 9: Review the Task 2 checkpoint**

```bash
git diff --check
git status --short
```

Expected: no whitespace errors; only the intended P05 additions plus preserved pre-existing changes appear. Do not stage mixed files.

### Task 3: Tenant-user lifecycle and transactional Key revocation

**Files:**
- Modify: `service/aiServeWeaveControlPlane/internal/model/model.go`
- Modify: `service/aiServeWeaveControlPlane/internal/store/store.go`
- Modify: `service/aiServeWeaveControlPlane/internal/store/memstore/memstore.go`
- Create: `service/aiServeWeaveControlPlane/internal/store/gormstore/users.go`
- Create: `service/aiServeWeaveControlPlane/internal/logic/users.go`
- Create: `service/aiServeWeaveControlPlane/internal/logic/users_test.go`
- Modify: `service/aiServeWeaveControlPlane/internal/handler/handlers.go`
- Modify: `service/aiServeWeaveControlPlane/internal/handler/routes.go`
- Modify: `service/aiServeWeaveControlPlane/internal/types/types.go`
- Create: `service/aiServeWeaveControlPlane/e2e/users_lifecycle_test.go`
- Modify: `service/aiServeWeaveControlPlane/internal/store/gormstore/mysql_live_test.go`

**Interfaces:**
- Consumes: `logic.Actor.SessionID` and Task 1 mutation gates.
- Produces: `store.UserLifecycle` methods that perform scoped point reads and atomic password/role/status/audit changes; disable returns affected Key hashes after revoking them in the same transaction.
- Produces: `Service.ChangeOwnPassword`, `ResetUserPassword`, `ChangeUserRole`, `DisableUser`, `EnableUser`, and `RevokeUserSessions`.
- Produces: tenant lifecycle endpoints listed in the approved spec.

- [ ] **Step 1: Write failing logic permission and behavior tests**

Use table-driven cases for owner/admin/member, self target, cross tenant, missing target, disabled target, same-state no-op, and API Key effects:

```go
func TestDisableUserRevokesSessionsAndCreatedKeys(t *testing.T) {
	f := newFixture(t)
	target := f.createUser(t, model.RoleMember)
	f.createKeyFor(t, target.ID)
	f.createSessionFor(t, target)
	if err := f.svc.DisableUser(context.Background(), f.ownerAt, target.ID); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
	if got := f.usableKeysByCreator(target.ID); got != 0 {
		t.Errorf("usable keys = %d, want 0", got)
	}
	if f.sessionValid(target.ID) { t.Error("session remains valid, want revoked") }
}
```

Name the protected mutations explicitly: dropping the tenant predicate, accepting admin, retaining a Key, allowing self-disable, or allowing the last owner to be demoted/disabled.

- [ ] **Step 2: Run logic tests and verify RED**

Run: `go test ./service/aiServeWeaveControlPlane/internal/logic -run 'Password|UserRole|DisableUser|EnableUser|RevokeUserSessions|LastOwner'`

Expected: FAIL because lifecycle methods do not exist.

- [ ] **Step 3: Add closed audit actions and store request/result types**

Add the approved action constants and narrow transaction inputs. Return `Changed`, the rendered target, and `RevokedKeyHashes`; never return Key plaintext or password hashes above the store boundary.

```go
type UserDisableResult struct {
	User             model.User
	Changed          bool
	RevokedKeyHashes []string
}
```

- [ ] **Step 4: Implement memory-store lifecycle transactions**

Hold one mutex across target lookup, active-owner invariant, mutation, Key revocation, and audit append. Tenant lookup must include both tenant id and user id. Repeated state writes return `Changed=false`.

- [ ] **Step 5: Implement gorm lifecycle transactions**

Use `db.Transaction`, `clause.Locking{Strength: "UPDATE"}`, current reads, named-column updates, and an audit insert on the same transaction handle. Lock the tenant's active owners before disabling or demoting one. Revoke keys with `tenant_id`, `created_by`, and `status=active` predicates, returning their hashes for post-commit cache invalidation.

- [ ] **Step 6: Implement logic with the Redis mutation protocol**

For password, role, disable, and enable: validate actor/input, begin the target's mutation gate, defer nonce-checked gate release, call the transaction method, then invalidate returned Key hashes. If gate creation fails, do not touch the database. Self password change first verifies the current password against the stored digest.

- [ ] **Step 7: Run logic tests and verify GREEN**

Run: `go test ./service/aiServeWeaveControlPlane/internal/logic`

Expected: PASS.

- [ ] **Step 8: Add request types, handlers, routes, and HTTP tests**

Use `{current_password,new_password}`, `{new_password}`, and `{role}` request types. Mutation handlers read actor and path id, call logic, return `204`, and never interpolate sensitive input into errors. Add `logic.ErrUnavailable` mapping to `503` and use `409` for protected-manager conflicts.

- [ ] **Step 9: Verify real-engine concurrency**

Extend the existing opt-in PostgreSQL/MySQL test harness so two concurrent attempts cannot disable/demote all active owners and disable atomically revokes creator Keys.

Run with each configured DSN: `go test -race ./service/aiServeWeaveControlPlane/internal/store/gormstore -run 'TestLiveUserLifecycle' -count=3`

Expected: exactly one conflicting last-owner mutation is refused; no tenant reaches zero active owners.

- [ ] **Step 10: Run tenant lifecycle HTTP tests**

Run: `go test ./service/aiServeWeaveControlPlane/e2e -run 'UserLifecycle|OwnPassword|LastOwner|DisableRevokesKeys'`

Expected: PASS.

- [ ] **Step 11: Review the Task 3 checkpoint**

```bash
git diff --check
git status --short
```

Expected: no whitespace errors and no unrelated file cleanup.

### Task 4: Platform-operator lifecycle

**Files:**
- Modify: `service/aiServeWeaveControlPlane/internal/store/store.go`
- Modify: `service/aiServeWeaveControlPlane/internal/store/memstore/memstore.go`
- Create: `service/aiServeWeaveControlPlane/internal/store/gormstore/operators.go`
- Modify: `service/aiServeWeaveControlPlane/internal/logic/platform.go`
- Modify: `service/aiServeWeaveControlPlane/internal/logic/platform_test.go`
- Modify: `service/aiServeWeaveControlPlane/internal/handler/handlers.go`
- Modify: `service/aiServeWeaveControlPlane/internal/handler/routes.go`
- Modify: `service/aiServeWeaveControlPlane/internal/types/types.go`
- Create: `service/aiServeWeaveControlPlane/e2e/operators_lifecycle_test.go`

**Interfaces:**
- Produces: `store.PlatformOperatorFilter{Status, Query string}` and paged list support.
- Produces: platform `CreatePlatformOperatorAs`, `ListPlatformOperators`, `ChangeOwnPlatformPassword`, `ResetPlatformPassword`, `DisablePlatformOperator`, `EnablePlatformOperator`, and `RevokePlatformOperatorSessions` logic methods.
- Produces: all `/operator/v1/auth/*` and `/operator/v1/operators*` endpoints from the spec, mounted regardless of Fleet/Registry configuration.

- [ ] **Step 1: Write failing platform lifecycle tests**

Cover a tenant actor, peer creation, list filtering/pagination, self-disable, last-active-operator concurrency, password reset, disabled login, enable, and session revocation. Split `requirePlatformActor` into identity-only authorization and the Registry-client check so account management does not require Registry configuration.

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test ./service/aiServeWeaveControlPlane/internal/logic ./service/aiServeWeaveControlPlane/e2e -run 'Platform.*(Lifecycle|Operator|Password|Session)'`

Expected: FAIL because management methods and routes are absent.

- [ ] **Step 3: Implement memory and gorm operator transactions**

Mirror tenant-user semantics without tenant ids or API Keys. Lock all active operators before disabling one, enforce at least one active operator, append audit in the same database transaction, and make disable/enable idempotent.

- [ ] **Step 4: Implement platform logic and HTTP endpoints**

Reuse the shared password functions and session mutation protocol. Management methods require `model.PlatformScope` plus `RolePlatformOperator`; only Registry node methods additionally require a configured Registry client. Responses contain the existing password-free operator wire type.

- [ ] **Step 5: Verify focused, e2e, and real-engine tests**

Run: `go test ./service/aiServeWeaveControlPlane/internal/logic ./service/aiServeWeaveControlPlane/e2e`

Run with each live DSN: `go test -race ./service/aiServeWeaveControlPlane/internal/store/gormstore -run 'TestLivePlatformOperatorLifecycle' -count=3`

Expected: PASS and exactly one concurrent last-operator disable is rejected.

- [ ] **Step 6: Review the Task 4 checkpoint**

```bash
git diff --check
git status --short
```

Expected: no whitespace errors and no unrelated file cleanup.

### Task 5: Console contracts, server session revocation, and allowlists

**Files:**
- Modify: `service/aiServeWeaveConsole/lib/console/contract.ts`
- Modify: `service/aiServeWeaveConsole/lib/console/contract.test.ts`
- Modify: `service/aiServeWeaveConsole/lib/console/permissions.ts`
- Modify: `service/aiServeWeaveConsole/lib/console/permissions.test.ts`
- Modify: `service/aiServeWeaveConsole/lib/console/upstream-routes.ts`
- Modify: `service/aiServeWeaveConsole/lib/console/upstream-routes.test.ts`
- Modify: `service/aiServeWeaveConsole/app/api/session/route.ts`
- Modify: `service/aiServeWeaveConsole/app/api/operator-session/route.ts`
- Modify: `service/aiServeWeaveConsole/lib/console/operator-client.ts`
- Create: `service/aiServeWeaveConsole/lib/console/user-lifecycle.ts`
- Create: `service/aiServeWeaveConsole/lib/console/user-lifecycle.test.ts`

**Interfaces:**
- Produces: parser types for platform-operator list envelopes.
- Produces: `canManageUsers(role)`, self-target helpers, fixed action labels, and bounded request builders.
- Produces: explicit allowlist entries for every tenant/platform lifecycle route.
- Changes: Console tenant and platform logout call the matching ControlPlane revoke endpoint before clearing the matching cookie.

- [ ] **Step 1: Write failing parser, permission, allowlist, and logout tests**

Include negative cases proving method/path confusion and encoded path segments do not broaden the proxy. For logout, inject a fake ControlPlane response and assert behavior rather than the fake itself: `204`/`401` clears the cookie; `503` leaves it and returns a stable availability error.

- [ ] **Step 2: Run Console tests and verify RED**

Run: `pnpm test -- lib/console/contract.test.ts lib/console/permissions.test.ts lib/console/upstream-routes.test.ts lib/console/user-lifecycle.test.ts`

Working directory: `service/aiServeWeaveConsole`

Expected: FAIL because contracts/routes/helpers are absent and logout is local-only.

- [ ] **Step 3: Implement parsers, permissions, and exact route entries**

Add only approved methods and paths. Tenant user actions use the Admin surface; operator accounts use the Operator surface. Query allowlists for operators are exactly `limit`, `cursor`, `status`, and `q`.

- [ ] **Step 4: Make logout server-side**

Read the sealed cookie server-side, call the correct ControlPlane endpoint with its JWT, and clear only the matching cookie on `204` or `401`. Do not pass token or upstream error text to the browser. Preserve the cookie on timeout, transport error, or `5xx`.

- [ ] **Step 5: Run Console unit tests and verify GREEN**

Run: `pnpm test`

Working directory: `service/aiServeWeaveConsole`

Expected: PASS.

- [ ] **Step 6: Review the Task 5 checkpoint**

```bash
git diff --check
git status --short
```

Expected: no whitespace errors; existing P02–P04 Console changes remain intact.

### Task 6: Tenant and platform lifecycle user interfaces

**Files:**
- Modify: `service/aiServeWeaveConsole/app/console/users/users-view.tsx`
- Create: `service/aiServeWeaveConsole/app/console/users/user-actions.tsx`
- Create: `service/aiServeWeaveConsole/app/console/account/page.tsx`
- Create: `service/aiServeWeaveConsole/app/console/account/account-security-view.tsx`
- Modify: `service/aiServeWeaveConsole/app/console/console-shell.tsx`
- Create: `service/aiServeWeaveConsole/app/operator/(protected)/operators/page.tsx`
- Create: `service/aiServeWeaveConsole/app/operator/(protected)/operators/operators-view.tsx`
- Create: `service/aiServeWeaveConsole/app/operator/(protected)/operators/operator-actions.tsx`
- Create: `service/aiServeWeaveConsole/app/operator/(protected)/account/page.tsx`
- Create: `service/aiServeWeaveConsole/app/operator/(protected)/account/account-security-view.tsx`
- Modify: `service/aiServeWeaveConsole/app/operator/operator-shell.tsx`

**Interfaces:**
- Consumes: Task 5 route helpers and existing `apiRequest`, `SubmitButton`, Dialog, Select, paging, and error components.
- Produces: owner-only tenant row actions, platform account management, and self account-security pages.

- [ ] **Step 1: Add the smallest reusable action state helpers under test**

Keep network/result-state logic in `lib/console/user-lifecycle.ts`; components only render dialogs and call it. Test request-result-unknown handling: write calls are never retried, and the list reloads before offering another state mutation.

- [ ] **Step 2: Implement tenant user actions**

Add role, status, and actions columns without changing server-side paging. Hide all management controls from admin/member and from the current owner row where the backend would return a self conflict. Disable confirmation must say existing Key revocation is permanent and Gateway cache may accept it for up to the configured TTL.

- [ ] **Step 3: Implement tenant account security**

Provide current/new password inputs and a separate revoke-all confirmation. Password values live only in component state for the duration of the request. On success call the Console session delete route, redirect to `/login`, and do not put a password in URL, storage, toast, or error text.

- [ ] **Step 4: Implement platform operator list and actions**

Use the existing pager/filter conventions. Do not render self-disable. Platform routes use `surface: "operator"` and never the tenant session.

- [ ] **Step 5: Implement platform account security and navigation**

Add operator and tenant account links to their own shells. Password change and revoke-all clear only `aisw_operator_session` and redirect to `/operator/login`.

- [ ] **Step 6: Run all Console gates**

Run in `service/aiServeWeaveConsole`:

```bash
pnpm lint
pnpm build
pnpm typecheck
pnpm test
```

Expected: all commands exit 0; build uses TS 6 and typecheck uses TS 7 as documented.

- [ ] **Step 7: Review the Task 6 checkpoint**

```bash
git diff --check
git status --short
```

Expected: no whitespace errors and no unrelated UI refactor.

### Task 7: Redis durability, documentation, and full verification

**Files:**
- Modify: `deploy/docker-compose.yaml`
- Modify: `deploy/README.md`
- Modify: `service/aiServeWeaveControlPlane/etc/controlplane.yaml`
- Modify: `service/aiServeWeaveControlPlane/README.md`
- Modify: `service/aiServeWeaveConsole/README.md`
- Modify: `service/aiServeWeaveConsole/STATUS.md`
- Modify: `service/aiServeWeaveConsole/AGENTS.md`
- Modify: `README.md`
- Modify: `AGENTS.md`
- Modify: `STATUS.md`

**Interfaces:**
- Changes: Compose Redis command becomes `redis-server --appendonly yes --appendfsync always`; the existing `redis-data:/data` volume remains.
- Produces: documented rollout, mandatory Redis dependency, pre-P05 JWT cut-over, fail-closed behavior, API permission matrix, API Key cache window, and reproducible acceptance evidence.

- [ ] **Step 1: Update deployment configuration and prose**

Change only the Redis durability flags and descriptions required by P05. Remove every statement that calls Redis optional for ControlPlane operation while preserving that the API Key verification cache itself still falls back to the database.

- [ ] **Step 2: Update API, security, and Console documentation**

Document the exact endpoints and role matrix, last-manager invariants, disabled-login indistinguishability, session cap, Redis outage status, logout semantics, Key revocation coupling, and the remaining P06 Gateway cache window.

- [ ] **Step 3: Perform the browser acceptance pass**

With a real ControlPlane, PostgreSQL/MySQL test deployment, and persistent Redis, verify the workflows named by the spec at desktop and 390 px widths. Confirm Escape restores focus from destructive dialogs and tenant/platform sessions remain independent.

- [ ] **Step 4: Run formatting and generated-code checks**

From the repository root:

```bash
gofmt -l ./service ./api
go generate ./api/...
git diff --check
```

Expected: `gofmt -l` prints nothing; generation adds no P05 protobuf diff; `git diff --check` exits 0.

- [ ] **Step 5: Run the complete Go gates**

```bash
go vet ./...
go build ./...
go test ./...
go test -race ./service/...
```

Expected: every command exits 0 with zero failing tests.

- [ ] **Step 6: Re-run complete Console gates freshly**

From `service/aiServeWeaveConsole`:

```bash
pnpm lint
pnpm build
pnpm typecheck
pnpm test
```

Expected: every command exits 0.

- [ ] **Step 7: Review the P05 requirement checklist and sensitive-data boundary**

Re-read the spec and inspect the final diff. Check every approved product rule against an implementation/test, verify no unrelated P02–P04 edits were overwritten, and search runtime logs/audit responses for forbidden credentials.

- [ ] **Step 8: Mark P05 complete only with evidence**

Update `STATUS.md` and service status documents with exact verified commands and real-service limitations. Do not mark P06 or P07 complete.

- [ ] **Step 9: Review the final delivery diff**

```bash
git diff --check
git status --short
```

Expected: all P05 work is present, pre-existing P02–P04 work is preserved, and no mixed commit is created without explicit user direction.
