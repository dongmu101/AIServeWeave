# P05 User and Session Lifecycle Design

## Objective

P05 completes the lifecycle of tenant users and platform operators: self-service password changes, administrative password resets, account disable/enable, tenant-role changes, and immediate session revocation. Disabling a tenant user also revokes every API Key that user created.

The implementation covers the ControlPlane and Console. It does not add Gateway revocation push; the remaining Gateway key-cache window belongs to P06.

## Approved product rules

### Tenant users

- Every active user may change their own password by presenting the current password. A successful change revokes all of that user's sessions, including the session making the request, so the Console requires a fresh login.
- Only an owner may reset another user's password, change another user's role, disable or enable another user, or revoke all of another user's sessions.
- An owner cannot disable themselves or change their own role through an administrative endpoint.
- A mutation must not leave a tenant without at least one active owner. This invariant is enforced under concurrent requests, not only checked in the Console.
- Disabling a user atomically marks the user disabled and revokes all active API Keys whose `created_by` is that user. Re-enabling the user never restores those keys.
- Password changes, password resets, role changes, and standalone session revocation do not revoke API Keys.
- Existing password rules remain unchanged: passwords are not trimmed and no new length, character-class, or non-empty restriction is introduced.

### Platform operators

- Platform operators can create and manage other platform operators. Management includes password reset, disable/enable, and all-session revocation.
- A platform operator may change their own password and revoke their own sessions.
- A platform operator cannot disable themselves or the last active platform operator.
- The BootstrapToken route remains available for the first platform operator and emergency recovery. The Console never receives or uses the BootstrapToken.
- Platform operators have no tenant role and own no tenant API Keys, so their lifecycle has no API Key side effect.

### Idempotency and visibility

- Disable, enable, and all-session revocation are idempotent. Repeating an already-achieved state returns success without adding a duplicate audit entry.
- Setting the existing role is likewise a successful no-op without a duplicate audit entry.
- A forbidden role returns `403`. A missing or cross-tenant target returns `404`. A last-owner, self-management, or last-platform-operator conflict returns `409`.
- Disabled-account login returns the same generic `401` response as an incorrect password, so account status cannot be enumerated from the public login endpoint.

## Session architecture

Redis is the authoritative online session store. There is no relational `sessions` table.

Every new JWT contains a cryptographically random `session_id` claim in addition to user id, tenant/scope, role, issued-at, and expiry. A protected request is accepted only when:

1. the JWT signature, algorithm, required claims, and expiry are valid;
2. a matching, unexpired Redis session exists;
3. the Redis record's subject kind, subject id, tenant/scope, and role exactly match the JWT claims; and
4. the subject is not inside a lifecycle-mutation login gate.

Tokens issued before P05 have no `session_id` and become invalid immediately after deployment. Users must sign in again. This deliberate cut-over ensures every accepted JWT can be revoked.

Tenant and platform sessions use the same implementation but different subject kinds. Their guards retain the existing two-way scope isolation: tenant tokens cannot reach `/operator/v1/*`, and platform tokens cannot reach tenant Admin endpoints.

### Redis keys and bounds

Keys are versioned and namespaced:

```text
aisw:session:v1:<session_id>
aisw:subject-sessions:v1:<subject_kind>:<subject_id>
aisw:subject-mutation:v1:<subject_kind>:<subject_id>
```

- A session value contains only the identity fields needed to compare with JWT claims. It never contains the signed JWT, password, API Key, or request body.
- The subject index is a sorted set scored by expiry. A Redis Lua script removes expired entries, creates the session, and enforces a maximum of 20 concurrent sessions per account. Creating session 21 deletes the earliest-expiring session.
- Every session key expires at the JWT expiry. Subject indexes are also expired and pruned, so abandoned indexes do not accumulate forever.
- Current-session logout deletes one session and its index member atomically.
- All-session revocation deletes at most 20 session keys and their subject index atomically. No request reads or buffers an unbounded collection.

## Lifecycle mutation protocol

A login can otherwise race between database authentication and a role, password, or status change. Lifecycle mutations therefore use a short-lived Redis login gate:

1. An atomic Lua operation creates a nonce-protected mutation gate and deletes all existing sessions for the subject.
2. Login session creation refuses while the gate exists, including a login that read the database immediately before step 1.
3. The ControlPlane performs the relational mutation. Last-owner and last-platform-operator checks happen inside the same database transaction and lock the relevant account set so concurrent changes cannot both pass.
4. On success, the gate is released only by the holder of its nonce. On database failure it is also released; if the process dies first, its short TTL prevents a permanent lockout.

Session revocation happens before the database mutation. A failed database mutation can therefore log the subject out, but cannot leave an old privileged session active. This is the approved fail-safe direction.

For tenant disable, the relational transaction updates the user and all active API Keys created by that user together. After commit, the existing verification cache entries for the affected hashes are invalidated. Gateway-local positive caches remain valid for at most their configured `-key-cache-ttl` until P06 supplies push invalidation.

## Redis availability and durability

Redis changes from an optional API Key cache to a required authentication dependency:

- An empty Redis address or a failed startup ping prevents the ControlPlane from starting.
- A Redis error during login, session validation, logout, or an identity mutation fails closed. Infrastructure failures return `503`; invalid, expired, or revoked sessions return `401`.
- Protected handlers never fall back to accepting a signed JWT without Redis validation.
- A Redis data loss that removes session keys logs users out; absence never makes a session valid.

The supplied Docker Compose deployment already mounts `/data` on a named volume and enables AOF. P05 pins it to `appendonly yes` and `appendfsync always`, because an acknowledged revocation must be durable before the relational change proceeds. Production deployments must provide equivalent persistence. Operators must not restore an older Redis snapshot that predates security mutations; persistence mode and replica failover determine the real Redis RPO and must be documented as deployment responsibility.

## HTTP API

Small mutation requests retain the existing 64 KiB body limit. Successful mutations return `204 No Content` unless listed otherwise.

### Tenant authentication and users

| Method and path | Request | Authorization and behavior |
| --- | --- | --- |
| `DELETE /admin/v1/auth/session` | none | Revokes the current tenant session. |
| `POST /admin/v1/auth/password` | `{current_password,new_password}` | Any active tenant user; changes their password and revokes all sessions. |
| `POST /admin/v1/auth/sessions/revoke` | none | Any active tenant user; revokes all of their sessions. |
| `PUT /admin/v1/users/:id/password` | `{new_password}` | Owner; resets another user's password and revokes that user's sessions. |
| `PUT /admin/v1/users/:id/role` | `{role}` | Owner; changes another user's role and revokes that user's sessions. |
| `POST /admin/v1/users/:id/disable` | none | Owner; disables another user, revokes sessions, and revokes their active API Keys. |
| `POST /admin/v1/users/:id/enable` | none | Owner; enables another user without restoring sessions or keys. |
| `POST /admin/v1/users/:id/sessions/revoke` | none | Owner; revokes all target-user sessions. |

The existing user list response remains the source for current `role` and `status`; no separate user-detail endpoint is added.

### Platform authentication and operators

| Method and path | Request | Authorization and behavior |
| --- | --- | --- |
| `DELETE /operator/v1/auth/session` | none | Revokes the current platform session. |
| `POST /operator/v1/auth/password` | `{current_password,new_password}` | Changes the current operator password and revokes all sessions. |
| `POST /operator/v1/auth/sessions/revoke` | none | Revokes all current-operator sessions. |
| `GET /operator/v1/operators` | pagination and `status`/`q` filters | Lists platform operators in the existing pagination envelope. |
| `POST /operator/v1/operators` | `{email,password,name}` | Creates another platform operator; returns `201`. |
| `PUT /operator/v1/operators/:id/password` | `{new_password}` | Resets another operator password and revokes their sessions. |
| `POST /operator/v1/operators/:id/disable` | none | Disables another operator and revokes their sessions. |
| `POST /operator/v1/operators/:id/enable` | none | Enables another operator. |
| `POST /operator/v1/operators/:id/sessions/revoke` | none | Revokes all target-operator sessions. |

The operator list is independent of Fleet and Registry configuration. It is always mounted behind `requirePlatformSession`.

## Data and store changes

- `users.status` and `platform_operators.status` remain the account-state source; no duplicate durable state is added to Redis.
- User and platform-operator stores gain scoped point reads and lifecycle mutations. Tenant-targeted reads always include `tenant_id` in the store query.
- API Key storage gains a transaction-capable operation to revoke every active key created by one tenant user and return only the hashes needed for cache invalidation.
- The relational mutation, affected-key revocation, and audit append for a security action execute in one database transaction. Existing unrelated audit behavior remains unchanged.
- The in-memory store implements the same contracts for deterministic default tests. PostgreSQL and MySQL implementations enforce the last-active-manager invariant under real concurrent transactions.

## Audit actions

Successful state changes append one audit event using closed action names:

```text
user.password.change
user.password.reset
user.role.change
user.disable
user.enable
user.sessions.revoke
platform_operator.create
platform_operator.password.change
platform_operator.password.reset
platform_operator.disable
platform_operator.enable
platform_operator.sessions.revoke
session.logout
platform_session.logout
```

Details may record old/new role and the number of revoked keys or sessions. They must not contain passwords, password hashes, signed JWTs, session ids, API Key hashes/plaintext, authentication headers, prompts, or workflow JSON.

## Console behavior

- `/console/users` adds owner-only row actions for password reset, role change, disable/enable, and all-session revocation. Destructive actions use confirmation dialogs and refresh the authoritative list after completion.
- A tenant account-security page lets every active user change their password and revoke all sessions. Either success clears the sealed Console cookie and redirects to login.
- `/operator/operators` lists and manages platform operators. The operator shell gains an account-security page for self-service password change and all-session revocation.
- Tenant and platform logout routes first ask the ControlPlane to revoke the current session. On `204` or `401`, they clear the matching local cookie. On a ControlPlane/Redis availability failure, they report failure and retain the cookie so the user can retry a server-side revocation.
- Tenant and platform cookies remain independent. A `401` clears only the cookie for the surface that received it.
- New Admin and Operator calls are added explicitly to the Console's method/path/query allowlists and covered by allowlist tests.
- UI messages state that disabling a user permanently revokes their existing API Keys, and that Gateway replicas may continue accepting a cached Key for up to the configured key-cache TTL until P06 is implemented.
- No device/session inventory UI is added in P05.

## Testing and acceptance

### Default automated tests

- Token tests prove `session_id` is mandatory and old-format JWTs fail.
- Redis-session contract tests cover create, validate, current logout, bulk revoke, exact claim matching, expiry, the 20-session cap, mutation gates, wrong nonces, and Redis errors.
- Logic tests cover every tenant and platform permission, self-management conflict, cross-tenant hiding, idempotent no-ops, disabled login, password verification, last-active-manager protection, session revocation, and user-disable Key revocation.
- HTTP tests cover every route, status code, bounded request body, tenant/platform guard isolation, `401` versus `503`, and secret-free error bodies.
- Console tests cover response parsing, permissions, route allowlists, cookie separation, session-invalid redirects, and logout behavior.

Tests use injected clocks and stores and never sleep to advance time. Test doubles assert consumer-visible behavior rather than their own call counts.

### Real-service verification

- Run lifecycle and last-manager concurrency tests against both PostgreSQL and MySQL.
- Run a real Redis test with AOF enabled, including ControlPlane restart, Redis restart, persisted active-session validation, persisted revocation, and fail-closed behavior while Redis is unavailable.
- Run the repository Go gates: formatting, vet, build, proto regeneration diff, tests, and race tests.
- Run Console lint, TypeScript 7 typecheck, Node tests, and the Next.js production build.
- In a real browser, verify tenant login, password change and forced re-login, owner role change, disable confirmation and Key warning, revoked-user rejection, re-enable, platform-operator management, independent tenant/platform logout, keyboard focus restoration, and a 390 px layout.
- Search logs and audit responses to confirm that passwords, JWTs, session ids, API Key hashes/plaintext, and authorization headers are absent.

## Documentation and rollout

Update `STATUS.md`, the root README/AGENTS code map where necessary, the ControlPlane README, Console README/STATUS/AGENTS, deployment README, sample ControlPlane configuration, and Docker Compose Redis command.

Rollout order is Redis persistence configuration first, then the ControlPlane, then the Console. Deploying the ControlPlane invalidates all pre-P05 JWTs. Gateway processes need no P05 code change; existing API Key cache behavior continues until P06.

## Out of scope

- Gateway push invalidation or a stronger API Key revocation SLA (P06).
- Device-level session inventory, device names, geolocation, or per-device trust.
- Password-policy changes, MFA, password-reset email, account deletion, tenant deletion, or platform-operator roles.
- Redis high-availability topology, backup orchestration, and disaster-recovery drills beyond documenting the required persistence semantics (P07/A02).
