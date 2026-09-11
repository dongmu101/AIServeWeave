# P07 Database Upgrade and Recovery Implementation Plan

> **For agentic workers:** Use superpowers:subagent-driven-development for the independent outbox task; the controller implements migrations and integration. Preserve the existing dirty worktree and do not commit unrelated changes.

**Goal:** Replace base-table AutoMigrate with verifiable migrations and complete durable revocation delivery plus database recovery acceptance.

**Architecture:** Keep existing SQL migration namespaces and published files for jobs/routes/templates, and share the checked runner across them. Add a checked, locked base-table namespace and a singleton transactional revocation outbox. Expose database-only operational commands and validate on disposable databases.

**Tech Stack:** Go 1.27, GORM, database/sql, PostgreSQL 18, MySQL 9.7, go-redis, runtime.Clock.

**Spec:** docs/superpowers/specs/2026-09-10-p07-database-upgrade-recovery-design.md

## Global Constraints

- 不新增第三方依赖，不改变数据面依赖边界或公共 HTTP/proto 契约。
- 新写与修改的注释英文在前、中文紧随；导出标识符有 doc comment。
- 不用真实 time.Sleep 推进测试时间，不记录凭据、哈希、Prompt 或工作流 JSON。
- 只在 P07 独立数据库和容器中进行故障注入与恢复，不修改已有服务数据。

## Task 1: Versioned base schema and operational entry points

Files: gormstore/basemigrate.go, migrations/base/{postgres,mysql}/*.sql, basemigrate_test.go, basemigrate_live_test.go; modify Store.Migrate, svc/servicecontext.go, config/config.go and main.go.

Interfaces: retain `Migrate(context.Context) error`; add `MigrateAll(context.Context, bool) error` (resume controls dirty retry), `CheckSchema(context.Context) error`, and `MigrationStatus(context.Context) ([]MigrationStatus, error)`. New migrations include singleton `key_revocation_outbox(id BIGINT PRIMARY KEY, generation BIGINT NOT NULL, delivered_generation BIGINT NOT NULL)` seeded with `(1,0,0)`.

- [x] Write tests rejecting checksum mismatch, unknown migrations and incomplete history, plus real-engine legacy-data fixtures before implementation.
- [x] Run focused tests and observe missing behavior; implement embedded migrations, dedicated-connection advisory lock, checksum/dirty history and explicit legacy column additions.
- [x] Add up/status/resume flags. Startup defaults to check; compatibility AutoMigrate applies fixed versions. Close resources on startup failures.
- [x] Verify empty/legacy database upgrade, no-op repeat, concurrent migration, dirty refusal/resume, and preservation of base data and unique constraints.

## Task 2: Durable coalesced revocation outbox

Files owned by this task: new gormstore/revocations.go, revocations_live_test.go, internal/revocationoutbox/*; modify only Store.RevokeAPIKey in gormstore.go, SetUserStatus in users.go, and cache/cache.go publication method. The controller owns migrations and svc assembly; do not edit those.

Interfaces: `(*gormstore.Store).FlushRevocations(ctx context.Context, publish func(context.Context) error) (bool, error)` locks singleton row and confirms only after successful publish. `(*cache.Verifications).PublishInvalidation(context.Context) error` performs existing INCR/PUBLISH with visible errors; existing void Invalidator methods remain compatible. `revocationoutbox.New(store Store, publisher Publisher, clock runtime.Clock) *Relay`, `(*Relay).Run(context.Context)`, and existing `Invalidate(context.Context,string)`/`InvalidateAll(context.Context)` method shapes allow production logic to trigger an immediate flush.

- [x] Write live tests proving key revoke/user disable rollback if outbox mutation fails, zero notifications for failed or unrelated changes, and persisted work after reconstructing Store.
- [x] Observe tests failing before changing the mutation paths; add generation increments within their existing transactions. RevokeAPIKey gains a transaction.
- [x] Implement FlushRevocations with row locking and at-least-once confirmation. Coalesce bursts; hold at most one row, no in-memory event queue.
- [x] Implement one-second background retries with injected Clock, a bounded per-attempt context and clean cancellation. Synchronous callbacks and background work share the same flush path; log only a fixed safe error category.
- [x] Verify Redis publisher failure retains pending generation, successful retry clears it, concurrent producers/consumers lose no final generation, repeated notification is harmless, and relay exits without leaks. Default tests remain offline; live tests use AISW_POSTGRES_TEST_DSN/AISW_MYSQL_TEST_DSN and can await controller-provisioned databases.

Do not dispatch subagents or commit. Report to /tmp/aisw-p07-outbox-report.md with files, focused test evidence and integration needs.

## Task 3: Recovery rehearsal, documentation and final verification

Files: deploy/database-recovery.md (new), control-plane README, deploy README/config comments, STATUS.md, CHANGELOG.md, and recovery live tests or scripts.

- [x] Run native logical backup/restore into separate empty P07 databases; preserve users, quota, Key statuses, audit, routing/template revisions, migration records and pending outbox; include MySQL job metadata.
- [x] Run schema status/check after restore; assert data matches and resumed outbox delivery. Exercise invalid restore/dirty migration refusal and document operator actions.
- [x] Update documents with exact runnable commands, failure semantics, Redis/Gateway recovery steps, unsupported down migration and remaining A02 boundaries.
- [x] Integrate relay lifecycle into svc and run focused tests, full gofmt/vet/build/generate/test/race gates. Review P07-only diff against the saved starting snapshot.

## Execution notes

Baseline control-plane default tests pass. Current checkout contains P05/P06 changes required by P07; work in place and retain them. Migration and outbox tasks share only gormstore.go (separate methods); integration consumes Relay's public interface. Migration DDL provides the outbox singleton before live tests or production service startup. No automatic commits are planned.

Acceptance completed 2026-09-11: all root Go gates passed; real PostgreSQL18/MySQL9.7/Redis8 full control-plane race passed, and all P07 cases passed again after schema review fixes (16.779s). CLI status verified 13/17 versions with Database-only configs. Outbox and full reviews approved; temporary containers removed. Existing P05/P06 changes remain uncommitted and preserved.
