# Console Platform Operations Implementation Plan

**Goal:** Complete approved P01 Console phase: independent platform authentication, fleet writes and platform audit.
**Architecture:** `/operator/*` owns a separate session cookie and layout. `/api/operator/*` forwards only allowlisted requests using the platform session. Tenant sessions remain independent. The control plane adds a platform-only audit read using existing pagination and storage.
**Tech stack:** Existing Next.js 16, React 19, TypeScript 6/7, Node test runner, Go control plane; no new dependencies.
**Spec:** User-approved design in this conversation, 2026-09-08.

## Constraints and workspace ruling

Preserve existing uncommitted P01 backend changes. Work in the shared checkout because those changes are the required API baseline; do not reset, commit or move them. Backend and UI workers own disjoint files. New or touched comments are bilingual. Browser JavaScript receives no upstream credentials. Read/write errors have fixed text; writes never automatically retry.

## Tasks

- [x] Backend: add `GET /operator/v1/audit` guarded by `requirePlatformSession`, reusing existing list handler and PlatformScope. Add e2e tests for unauthenticated/tenant rejection, platform pagination and tenant/platform audit isolation.
- [x] Session and transport: add `operator-session.ts` contract/crypto and server cookie module; test tamper, expiry, cross-cookie rejection. Add `/api/operator-session` and replace shared-secret operator forwarding with platform cookie. Add allowlist tests for node writes, audit filters and denied paths.
- [x] Pages: `/operator/login`, independent layout/shell, fleet states/actions, models, workflow publication view, audit. Existing tenant links point to operator entry; tenant workflow page no longer fetches operator data. Legacy fleet/models routes redirect to the new surface.
- [x] Validation: run Console lint, build, typecheck, tests; Go format, vet, build, generate consistency, tests, service race tests. Review all new changes, update README/AGENTS/STATUS with evidence and remaining limitations.

## Verification examples

`resolveOperatorUpstream("POST", ["operator", "v1", "nodes", "node-1", "disable"], new URLSearchParams())` must resolve; the equivalent tenant resolver must return null. `openOperator(tenantCookie, key)` and tenant `open(operatorCookie, key)` must return null. Expiry is tested with supplied timestamps, never sleeps. UI actions refresh Registry state and fleet snapshots independently, and do not claim propagation based on a successful write alone.

## Results and review

Backend and UI tasks were implemented by separate workers and reviewed. Scope/auth tests passed. Review found login return-path loss and an overly restrictive generic node-ID placeholder; both fixed, with safe platform return allowlist and a dedicated node-label validator. Browser audit copy corrected after smoke inspection. Node labels containing path separators remain unsupported by the existing backend HTTP route and are explicitly disabled in the UI.

Go gates passed. Console lint/typecheck/tests and Webpack production build passed; default Turbopack worker port binding is blocked by this environment. Isolated local HTTP/browser smoke verified two sessions, CSRF, cookie separation, node mutation, approval UI and narrow layout. No commit or merge performed; pre-existing backend work preserved.
