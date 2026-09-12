# P09a 请求与错误检索(C28)Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist a redacted record of every authenticated OpenAI front-door request (chat/responses/embeddings/models) and expose it for paginated, filtered search on both the tenant Console and the platform operator Console, with a bounded retention period.

**Architecture:** Gateway gains a request-logging middleware (after auth resolves tenant identity, before rate limiting) that classifies the response by HTTP status code into a closed outcome enum, buffers records in a bounded channel, and pushes them in batches to a new control-plane internal API (`POST /internal/v1/requestlogs`, InternalToken-guarded, idempotent via primary-key conflict skip). The control plane persists them in a new `request_logs` table (dual PostgreSQL/MySQL, following `metrics_history`'s precedent, not `jobs`'s MySQL-only one) and serves two read endpoints — `GET /admin/v1/requests` (tenant-scoped) and `GET /operator/v1/requests` (cross-tenant) — using the existing keyset-pagination (`store.ListQuery`/`Page[T]`/`readPage`) and retention-cleanup (`metricshistory.Retention`-style) infrastructure. Console adds one shared list view component reused by a tenant page and an operator page, mirroring `AuditView`'s existing `surface` pattern exactly.

**Tech Stack:** Go 1.27 (Gateway, ControlPlane — go-zero `rest.Server` with a handwritten route table, GORM), TypeScript/Next.js 16 App Router (Console), no new third-party dependencies.

**Spec:** [docs/superpowers/specs/2026-09-11-p09-request-search-design.md](../specs/2026-09-11-p09-request-search-design.md)

## Global Constraints

- Only OpenAI front-door requests (chat/responses/embeddings/models) are recorded; workflow Job requests already have J07's persisted history and are out of scope.
- Only requests that resolved a tenant identity via the control-plane `Verifier` are recorded — no verifier configured, static `-api-keys` mode, and rejected/unauthenticated attempts are never persisted (no tenant to attribute them to).
- No free-text error message, full API key, full Prompt, or workflow JSON may ever be written to the new table — outcome is a closed enum derived purely from the HTTP status code.
- The Gateway → control plane push is asynchronous and bounded: a full buffer drops the newest record and increments a counter; it must never block or slow down an inference request.
- New table `request_logs` is dual PostgreSQL/MySQL (follows `metrics_history`'s precedent), migrated via the existing fixed-version SQL ledger (`migrateOne`/`migrationNamespaces`), and is **not** added to `checkSchemaObjects`'s strict drift check — same exclusion `metrics_history_points` already has.
- Default retention is 30 days, cleaned up by an unconditional background goroutine in the control plane (no `Enabled()` gate — the table always exists once migrated).
- No new third-party dependency: Gateway's push client uses the same `net/http`-only pattern as `controlplaneclient.JobsClient`; the bounded buffer is a plain `chan`.
- Every new/changed doc comment and inline comment follows the repo's bilingual convention (English first, Chinese immediately after, `//` one line each).
- Table-driven tests with a readable `name` field; no real `time.Sleep` — inject `runtime.Clock`.

---

### Task 1: ControlPlane model, migration and namespace registration

**Files:**
- Create: `service/aiServeWeaveControlPlane/internal/model/requestlog.go`
- Create: `service/aiServeWeaveControlPlane/internal/store/gormstore/migrations/request_logs/postgres/0001_request_logs.sql`
- Create: `service/aiServeWeaveControlPlane/internal/store/gormstore/migrations/request_logs/mysql/0001_request_logs.sql`
- Create: `service/aiServeWeaveControlPlane/internal/store/gormstore/requestlogmigrate.go`
- Modify: `service/aiServeWeaveControlPlane/internal/store/gormstore/migrate.go:55` (add `"request_logs"` to the `groups` slice in `migrationNamespaces`)
- Test: `service/aiServeWeaveControlPlane/internal/store/gormstore/requestlogmigrate_test.go`

**Interfaces:**
- Produces: `model.RequestLog` struct (`ID`, `TenantID`, `KeyDisplay`, `Endpoint`, `StatusCode`, `Outcome`, `DurationMS`, `CreatedAt`), `model.RequestLog.TableName() string`, `(*gormstore.Store).MigrateRequestLogs(ctx) ([]string, error)`.

- [ ] **Step 1: Write `model.RequestLog`**

```go
// requestlog.go holds the persisted record of one authenticated OpenAI
// front-door request, per STATUS.md's P09/C28.
//
// requestlog.go 保存一次已通过鉴权的 OpenAI 前门请求的持久化记录，对应
// STATUS.md 的 P09/C28。
package model

import "time"

// RequestLog is one authenticated chat/responses/embeddings/models request,
// as a Gateway replica reported it. ID is the request-correlation id
// common/reqid minted for it on the Gateway side, not a surrogate key — the
// same choice Job.ID already makes, and for the same reason: the id is
// already globally unique and is what every caller already refers to the
// request by, so a duplicate report (a retried batch push) is naturally
// rejected — here, silently skipped — by the same uniqueness this key
// already provides.
//
// Outcome is a closed classification derived purely from StatusCode by the
// Gateway before this row ever exists; no free-text error message is ever
// stored here.
//
// RequestLog 是一次已通过鉴权的 chat/responses/embeddings/models 请求，由
// Gateway 副本上报。ID 是 Gateway 一侧 common/reqid 为它铸造的请求关联
// id，不是代理键——与 Job.ID 相同的选择，理由也相同：这个 id 本就全局唯一，
// 也是每个调用方已经用来指代这次请求的东西，因此重复上报（一次被重试的批量
// 推送）天然会被这同一个唯一性拒绝——这里的表现是被悄悄跳过。
//
// Outcome 是 Gateway 在这一行存在之前就已从 StatusCode 纯粹推导出的封闭分类；
// 这里从不存储任何自由文本错误信息。
type RequestLog struct {
	ID         string `gorm:"primaryKey;size:64"`
	TenantID   string `gorm:"size:32;not null"`
	KeyDisplay string `gorm:"size:64;not null"`
	Endpoint   string `gorm:"size:16;not null"`
	StatusCode int    `gorm:"not null"`
	Outcome    string `gorm:"size:24;not null"`
	DurationMS int64  `gorm:"not null"`
	CreatedAt  time.Time `gorm:"not null"`
}

func (RequestLog) TableName() string { return "request_logs" }
```

- [ ] **Step 2: Write the migration SQL**

`migrations/request_logs/postgres/0001_request_logs.sql`:

```sql
CREATE TABLE request_logs (
 id VARCHAR(64) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 key_display VARCHAR(64) NOT NULL,
 endpoint VARCHAR(16) NOT NULL,
 status_code SMALLINT NOT NULL,
 outcome VARCHAR(24) NOT NULL,
 duration_ms BIGINT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_request_logs_tenant_created ON request_logs (tenant_id, created_at, id);
CREATE INDEX idx_request_logs_tenant_outcome_created ON request_logs (tenant_id, outcome, created_at, id);
```

`migrations/request_logs/mysql/0001_request_logs.sql`:

```sql
CREATE TABLE request_logs (
 id VARCHAR(64) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 key_display VARCHAR(64) NOT NULL,
 endpoint VARCHAR(16) NOT NULL,
 status_code SMALLINT NOT NULL,
 outcome VARCHAR(24) NOT NULL,
 duration_ms BIGINT NOT NULL,
 created_at DATETIME(6) NOT NULL,
 INDEX idx_request_logs_tenant_created (tenant_id, created_at, id),
 INDEX idx_request_logs_tenant_outcome_created (tenant_id, outcome, created_at, id)
) ENGINE=InnoDB;
```

- [ ] **Step 3: Register the namespace and add the convenience migrate function**

Edit `migrate.go:54-59`:

```go
func migrationNamespaces(dialect string) []string {
	groups := []string{"base", "routes", "workflow_templates", "metrics_history", "request_logs"}
	if dialect == "mysql" {
		groups = append(groups, "jobs")
	}
	return groups
}
```

`requestlogmigrate.go`:

```go
package gormstore

import "context"

// MigrateRequestLogs applies the request_logs versioned SQL under the shared
// migration lock. It exists as a narrow, single-namespace entry point for
// live tests — production startup goes through MigrateAll, which already
// includes "request_logs" via migrationNamespaces.
//
// MigrateRequestLogs 在共享迁移锁下应用 request_logs 的版本化 SQL。它是供
// 独立测试使用的窄入口——生产启动走 MigrateAll，其中已经通过
// migrationNamespaces 包含了 "request_logs"。
func (s *Store) MigrateRequestLogs(ctx context.Context) ([]string, error) {
	return s.migrateOne(ctx, "request_logs")
}
```

- [ ] **Step 4: Write a migration-status unit test (no real database)**

```go
package gormstore

import "testing"

func TestRequestLogsNamespaceIsRegisteredForBothDialects(t *testing.T) {
	tests := []struct {
		name    string
		dialect string
	}{
		{name: "postgres includes request_logs", dialect: "postgres"},
		{name: "mysql includes request_logs", dialect: "mysql"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groups := migrationNamespaces(tt.dialect)
			found := false
			for _, g := range groups {
				if g == "request_logs" {
					found = true
				}
			}
			if !found {
				t.Fatalf("migrationNamespaces(%q) = %v, want it to include \"request_logs\"", tt.dialect, groups)
			}
		})
	}
}

func TestLoadRequestLogsMigrationsParsesEmbeddedSQL(t *testing.T) {
	for _, dialect := range []string{"postgres", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			files, err := loadMigrations("request_logs", dialect)
			if err != nil {
				t.Fatalf("loadMigrations(request_logs, %s) error = %v, want nil", dialect, err)
			}
			if len(files) != 1 {
				t.Fatalf("loadMigrations(request_logs, %s) returned %d files, want 1", dialect, len(files))
			}
		})
	}
}
```

- [ ] **Step 5: Run the new tests**

Run: `go test ./service/aiServeWeaveControlPlane/internal/store/gormstore/... -run RequestLogs -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add service/aiServeWeaveControlPlane/internal/model/requestlog.go \
  service/aiServeWeaveControlPlane/internal/store/gormstore/migrations/request_logs \
  service/aiServeWeaveControlPlane/internal/store/gormstore/requestlogmigrate.go \
  service/aiServeWeaveControlPlane/internal/store/gormstore/requestlogmigrate_test.go \
  service/aiServeWeaveControlPlane/internal/store/gormstore/migrate.go
git commit -m "$(cat <<'EOF'
feat(controlplane): add request_logs table and migration namespace

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: `store.RequestLogs` interface and embedding into `store.Store`

**Files:**
- Modify: `service/aiServeWeaveControlPlane/internal/store/store.go` (add `RequestLogFilter`, `RequestLogs` interface, embed into `Store`)

**Interfaces:**
- Consumes: `model.RequestLog` (Task 1).
- Produces: `store.RequestLogFilter{TenantID, RequestID, Outcome, Since, Until}`, `store.RequestLogs` interface with `CreateRequestLogs(ctx, []model.RequestLog) error`, `ListRequestLogs(ctx, ListQuery, RequestLogFilter) (Page[model.RequestLog], error)`, `DeleteRequestLogsBefore(ctx, time.Time) (int64, error)`.

- [ ] **Step 1: Add the filter type and interface, near the existing `Jobs`/`JobFilter` declarations (around store.go:369-412)**

```go
// RequestLogFilter narrows a RequestLogs listing. An empty TenantID means
// every tenant — the operator cross-tenant search path (STATUS.md's P09/C28)
// — while every tenant-scoped caller sets it to the caller's own tenant.
//
// RequestLogFilter 收窄一次 RequestLogs 列举。TenantID 为空表示不限租户——对应
// STATUS.md P09/C28 的运维跨租户检索路径——而每个按租户限定的调用方都会把它
// 设为调用方自己的租户。
type RequestLogFilter struct {
	TenantID  string
	RequestID string
	Outcome   string
	Since     time.Time
	Until     time.Time
}

// RequestLogs persists the control plane's record of one authenticated
// OpenAI front-door request, per STATUS.md's P09/C28 and the design doc's
// request-search contract. Unlike Jobs, a row here is a one-shot append: no
// update method exists because a request's outcome is already final by the
// time the Gateway reports it.
//
// RequestLogs 持久化控制面对一次已通过鉴权的 OpenAI 前门请求的记录，对应
// STATUS.md 的 P09/C28 与设计文档的请求检索契约。与 Jobs 不同，这里的一行是
// 一次性追加：不存在更新方法，因为 Gateway 上报时这次请求的结果已经是终态。
type RequestLogs interface {
	// CreateRequestLogs inserts a batch of records, silently skipping any
	// whose id already exists — the batch push's own idempotency, since the
	// Gateway may report the same record more than once across retries of
	// an ambiguous earlier push.
	//
	// CreateRequestLogs 插入一批记录，静默跳过任何 id 已存在的记录——这就是
	// 批量推送自身的幂等性，因为 Gateway 可能会在一次结果不明的早先推送之后
	// 重复上报同一条记录。
	CreateRequestLogs(ctx context.Context, records []model.RequestLog) error
	// ListRequestLogs reads one page, newest first.
	//
	// ListRequestLogs 读取一页，最新的在前。
	ListRequestLogs(ctx context.Context, query ListQuery, filter RequestLogFilter) (Page[model.RequestLog], error)
	// DeleteRequestLogsBefore removes every row older than before, for the
	// retention sweeper.
	//
	// DeleteRequestLogsBefore 删除每一行早于 before 的记录，供保留期清理协程
	// 使用。
	DeleteRequestLogsBefore(ctx context.Context, before time.Time) (int64, error)
}
```

- [ ] **Step 2: Embed it into `Store` (around store.go:515-526)**

```go
type Store interface {
	Routes
	WorkflowTemplates
	Tenants
	Users
	PlatformOperators
	APIKeys
	Audit
	Jobs
	JobArtifacts
	MetricsHistory
	RequestLogs
}
```

- [ ] **Step 3: Confirm the package still compiles (implementations follow in the next two tasks)**

Run: `go build ./service/aiServeWeaveControlPlane/... 2>&1 | head -30`
Expected: FAIL — `*gormstore.Store` and `*memstore.Store` do not implement `store.RequestLogs` yet. This is expected; Tasks 3 and 4 fix it.

- [ ] **Step 4: Commit**

```bash
git add service/aiServeWeaveControlPlane/internal/store/store.go
git commit -m "$(cat <<'EOF'
feat(controlplane): declare the store.RequestLogs interface

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `gormstore` implementation and real-database live test

**Files:**
- Create: `service/aiServeWeaveControlPlane/internal/store/gormstore/requestlogs.go`
- Create: `service/aiServeWeaveControlPlane/internal/store/gormstore/requestlogs_live_test.go`

**Interfaces:**
- Consumes: `store.RequestLogFilter`, `model.RequestLog`, `readPage` (existing generic in `gormstore.go`), `clause.OnConflict` (`gorm.io/gorm/clause`).
- Produces: `(*Store).CreateRequestLogs`, `(*Store).ListRequestLogs`, `(*Store).DeleteRequestLogsBefore` — implements `store.RequestLogs`.

- [ ] **Step 1: Write the implementation**

```go
package gormstore

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreateRequestLogs implements store.RequestLogs.
func (s *Store) CreateRequestLogs(ctx context.Context, records []model.RequestLog) error {
	if len(records) == 0 {
		return nil
	}
	return translate(s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&records).Error)
}

// ListRequestLogs implements store.RequestLogs.
func (s *Store) ListRequestLogs(ctx context.Context, query store.ListQuery, filter store.RequestLogFilter) (store.Page[model.RequestLog], error) {
	db := s.db.WithContext(ctx).Model(&model.RequestLog{})
	if filter.TenantID != "" {
		db = db.Where("tenant_id = ?", filter.TenantID)
	}
	if filter.RequestID != "" {
		db = db.Where("id = ?", filter.RequestID)
	}
	if filter.Outcome != "" {
		db = db.Where("outcome = ?", filter.Outcome)
	}
	if !filter.Since.IsZero() {
		db = db.Where("created_at >= ?", filter.Since)
	}
	if !filter.Until.IsZero() {
		db = db.Where("created_at < ?", filter.Until)
	}
	return readPage(db, query, func(r model.RequestLog) (time.Time, string) { return r.CreatedAt, r.ID })
}

// DeleteRequestLogsBefore implements store.RequestLogs.
func (s *Store) DeleteRequestLogsBefore(ctx context.Context, before time.Time) (int64, error) {
	res := s.db.WithContext(ctx).Where("created_at < ?", before).Delete(&model.RequestLog{})
	return res.RowsAffected, translate(res.Error)
}
```

- [ ] **Step 2: Write the live test, following `metricshistory_live_test.go`'s exact pattern**

This package's convention (confirmed from `metricshistory_live_test.go`) is: external test package `gormstore_test`, a shared `migrationDatabases(t, func(t *testing.T, db *gorm.DB, dialect string) {...})` helper that iterates both configured dialects and skips cleanly when no DSN env var is set, `gormstore.New(db)` to build the store, and `st.MigrateAll(ctx, false)` rather than a single-namespace migrate call. Follow that exactly:

```go
package gormstore_test

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
)

func TestLiveRequestLogsCreateIsIdempotentOnDuplicateID(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, dialect string) {
		st := gormstore.New(db)
		ctx := context.Background()
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatalf("MigrateAll() error = %v", err)
		}

		record := model.RequestLog{
			ID: "req_dup_1", TenantID: "tnt_1", KeyDisplay: "aisw-abcd1234",
			Endpoint: "chat", StatusCode: 200, Outcome: "ok", DurationMS: 42,
			CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
		}
		if err := st.CreateRequestLogs(ctx, []model.RequestLog{record}); err != nil {
			t.Fatalf("first CreateRequestLogs() error = %v, want nil", err)
		}
		// A retried push carrying the same id must not error and must not
		// duplicate the row.
		if err := st.CreateRequestLogs(ctx, []model.RequestLog{record}); err != nil {
			t.Fatalf("duplicate CreateRequestLogs() error = %v, want nil (silently skipped)", err)
		}

		page, err := st.ListRequestLogs(ctx, store.ListQuery{}, store.RequestLogFilter{TenantID: "tnt_1"})
		if err != nil {
			t.Fatalf("ListRequestLogs() error = %v, want nil", err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("ListRequestLogs() returned %d items, want exactly 1 despite the duplicate push", len(page.Items))
		}
	})
}

func TestLiveRequestLogsFiltersAndPaginatesByTenant(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, dialect string) {
		st := gormstore.New(db)
		ctx := context.Background()
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatalf("MigrateAll() error = %v", err)
		}

		base := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
		records := []model.RequestLog{
			{ID: "req_a1", TenantID: "tnt_a", KeyDisplay: "aisw-a", Endpoint: "chat", StatusCode: 200, Outcome: "ok", DurationMS: 10, CreatedAt: base},
			{ID: "req_a2", TenantID: "tnt_a", KeyDisplay: "aisw-a", Endpoint: "chat", StatusCode: 429, Outcome: "rate_limited", DurationMS: 5, CreatedAt: base.Add(time.Minute)},
			{ID: "req_b1", TenantID: "tnt_b", KeyDisplay: "aisw-b", Endpoint: "embeddings", StatusCode: 200, Outcome: "ok", DurationMS: 8, CreatedAt: base.Add(2 * time.Minute)},
		}
		if err := st.CreateRequestLogs(ctx, records); err != nil {
			t.Fatalf("CreateRequestLogs() error = %v, want nil", err)
		}

		page, err := st.ListRequestLogs(ctx, store.ListQuery{}, store.RequestLogFilter{TenantID: "tnt_a"})
		if err != nil {
			t.Fatalf("ListRequestLogs(tnt_a) error = %v, want nil", err)
		}
		if len(page.Items) != 2 {
			t.Fatalf("ListRequestLogs(tnt_a) returned %d items, want 2 (tnt_b's row must not leak in)", len(page.Items))
		}

		page, err = st.ListRequestLogs(ctx, store.ListQuery{}, store.RequestLogFilter{Outcome: "rate_limited"})
		if err != nil {
			t.Fatalf("ListRequestLogs(outcome=rate_limited) error = %v, want nil", err)
		}
		if len(page.Items) != 1 || page.Items[0].ID != "req_a2" {
			t.Fatalf("ListRequestLogs(outcome=rate_limited) = %+v, want exactly req_a2", page.Items)
		}
	})
}
```

Before finalizing, open `metricshistory_live_test.go` once more to copy `migrationDatabases`'s exact signature verbatim (the sketch above reflects it as read during planning, but confirm at implementation time since it is this file's shared fixture, not something this task defines).

- [ ] **Step 3: Run the unit-level tests without a real database (they should skip cleanly)**

Run: `go test ./service/aiServeWeaveControlPlane/internal/store/gormstore/... -run RequestLogsLive -v`
Expected: `--- SKIP` (no `AISW_..._TEST_DSN` set), not a failure.

- [ ] **Step 4: If a real PostgreSQL or MySQL is available, run against it**

Run (example for MySQL, matching the repo's existing env var convention found in the file you read in Step 2): `AISW_MYSQL_TEST_DSN=... go test ./service/aiServeWeaveControlPlane/internal/store/gormstore/... -run RequestLogsLive -v -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add service/aiServeWeaveControlPlane/internal/store/gormstore/requestlogs.go \
  service/aiServeWeaveControlPlane/internal/store/gormstore/requestlogs_live_test.go
git commit -m "$(cat <<'EOF'
feat(controlplane): implement request_logs persistence in gormstore

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `memstore` implementation and business-rule unit tests

**Files:**
- Modify: `service/aiServeWeaveControlPlane/internal/store/memstore/memstore.go` (add a `requestLogs map[string]model.RequestLog` field to the `Store` struct, initialized in `New`)
- Create: `service/aiServeWeaveControlPlane/internal/store/memstore/requestlogs.go`
- Create: `service/aiServeWeaveControlPlane/internal/store/memstore/requestlogs_test.go`

**Interfaces:**
- Consumes: `paginate`, `sortNewestFirst` (existing generics in this package, used by `ListJobs`).
- Produces: `(*memstore.Store).CreateRequestLogs/ListRequestLogs/DeleteRequestLogsBefore` — implements `store.RequestLogs`, completing the `var _ store.Store = (*Store)(nil)` assertion.

- [ ] **Step 1: Add the field**

Find the `Store` struct definition and its `jobs map[string]model.Job` field in `memstore.go`, and add next to it:

```go
	requestLogs map[string]model.RequestLog
```

Find `New()`'s initialization of `jobs: make(map[string]model.Job)` and add alongside it:

```go
		requestLogs: make(map[string]model.RequestLog),
```

- [ ] **Step 2: Write the implementation**

```go
package memstore

import (
	"context"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreateRequestLogs implements store.RequestLogs, mirroring CreateJob's
// duplicate-id handling except that a duplicate here is a silent skip
// (idempotent batch push), never store.ErrConflict — nothing downstream
// reads back a single created record the way CreateJob's caller does.
//
// CreateRequestLogs 实现 store.RequestLogs，其对重复 id 的处理方式与
// CreateJob 相仿，只是这里重复是静默跳过（幂等的批量推送），而不是
// store.ErrConflict——不像 CreateJob 那样，没有下游会读回单条创建结果。
func (s *Store) CreateRequestLogs(_ context.Context, records []model.RequestLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range records {
		if _, exists := s.requestLogs[r.ID]; exists {
			continue
		}
		s.requestLogs[r.ID] = r
	}
	return nil
}

// ListRequestLogs implements store.RequestLogs.
func (s *Store) ListRequestLogs(_ context.Context, query store.ListQuery, filter store.RequestLogFilter) (store.Page[model.RequestLog], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.RequestLog
	for _, r := range s.requestLogs {
		if filter.TenantID != "" && r.TenantID != filter.TenantID {
			continue
		}
		if filter.RequestID != "" && r.ID != filter.RequestID {
			continue
		}
		if filter.Outcome != "" && r.Outcome != filter.Outcome {
			continue
		}
		if !filter.Since.IsZero() && r.CreatedAt.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && !r.CreatedAt.Before(filter.Until) {
			continue
		}
		out = append(out, r)
	}
	sortNewestFirst(out, func(r model.RequestLog) (time.Time, string) { return r.CreatedAt, r.ID })
	return paginate(out, query, func(r model.RequestLog) (time.Time, string) { return r.CreatedAt, r.ID })
}

// DeleteRequestLogsBefore implements store.RequestLogs.
func (s *Store) DeleteRequestLogsBefore(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed int64
	for id, r := range s.requestLogs {
		if r.CreatedAt.Before(before) {
			delete(s.requestLogs, id)
			removed++
		}
	}
	return removed, nil
}
```

- [ ] **Step 3: Write the failing tests first (table-driven), then confirm the implementation above makes them pass**

This package's test convention (confirmed from `jobs_test.go`) is the external test package `memstore_test`, calling only the exported surface:

```go
package memstore_test

import (
	"context"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
)

func TestCreateRequestLogsSkipsADuplicateIDSilently(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	rec := model.RequestLog{ID: "req_1", TenantID: "tnt_1", Endpoint: "chat", StatusCode: 200, Outcome: "ok", CreatedAt: time.Now()}

	if err := s.CreateRequestLogs(ctx, []model.RequestLog{rec}); err != nil {
		t.Fatalf("first CreateRequestLogs() error = %v, want nil", err)
	}
	dup := rec
	dup.StatusCode = 500 // a different payload under the same id must not overwrite
	if err := s.CreateRequestLogs(ctx, []model.RequestLog{dup}); err != nil {
		t.Fatalf("duplicate CreateRequestLogs() error = %v, want nil (silent skip, not an error)", err)
	}

	page, err := s.ListRequestLogs(ctx, store.ListQuery{}, store.RequestLogFilter{TenantID: "tnt_1"})
	if err != nil {
		t.Fatalf("ListRequestLogs() error = %v, want nil", err)
	}
	if len(page.Items) != 1 || page.Items[0].StatusCode != 200 {
		t.Fatalf("ListRequestLogs() = %+v, want exactly one row with the original StatusCode 200 (duplicate must not overwrite)", page.Items)
	}
}

func TestListRequestLogsFiltersByTenantOutcomeAndTime(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)
	records := []model.RequestLog{
		{ID: "req_a1", TenantID: "tnt_a", Endpoint: "chat", StatusCode: 200, Outcome: "ok", CreatedAt: base},
		{ID: "req_a2", TenantID: "tnt_a", Endpoint: "chat", StatusCode: 429, Outcome: "rate_limited", CreatedAt: base.Add(time.Minute)},
		{ID: "req_b1", TenantID: "tnt_b", Endpoint: "embeddings", StatusCode: 200, Outcome: "ok", CreatedAt: base.Add(2 * time.Minute)},
	}
	if err := s.CreateRequestLogs(ctx, records); err != nil {
		t.Fatalf("CreateRequestLogs() error = %v, want nil", err)
	}

	tests := []struct {
		name   string
		filter store.RequestLogFilter
		wantIDs []string
	}{
		{name: "tenant scope excludes other tenants", filter: store.RequestLogFilter{TenantID: "tnt_a"}, wantIDs: []string{"req_a2", "req_a1"}},
		{name: "empty tenant means every tenant (operator view)", filter: store.RequestLogFilter{}, wantIDs: []string{"req_b1", "req_a2", "req_a1"}},
		{name: "outcome filter", filter: store.RequestLogFilter{Outcome: "rate_limited"}, wantIDs: []string{"req_a2"}},
		{name: "request id exact match", filter: store.RequestLogFilter{RequestID: "req_b1"}, wantIDs: []string{"req_b1"}},
		{name: "since excludes earlier rows", filter: store.RequestLogFilter{Since: base.Add(90 * time.Second)}, wantIDs: []string{"req_b1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := s.ListRequestLogs(ctx, store.ListQuery{}, tt.filter)
			if err != nil {
				t.Fatalf("ListRequestLogs(%+v) error = %v, want nil", tt.filter, err)
			}
			gotIDs := make([]string, len(page.Items))
			for i, r := range page.Items {
				gotIDs[i] = r.ID
			}
			if len(gotIDs) != len(tt.wantIDs) {
				t.Fatalf("ListRequestLogs(%+v) = %v, want %v", tt.filter, gotIDs, tt.wantIDs)
			}
			for i := range gotIDs {
				if gotIDs[i] != tt.wantIDs[i] {
					t.Fatalf("ListRequestLogs(%+v)[%d] = %q, want %q (newest first)", tt.filter, i, gotIDs[i], tt.wantIDs[i])
				}
			}
		})
	}
}

func TestDeleteRequestLogsBeforeRemovesOnlyOlderRows(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	cutoff := time.Now()
	records := []model.RequestLog{
		{ID: "req_old", TenantID: "tnt_1", CreatedAt: cutoff.Add(-2 * time.Hour)},
		{ID: "req_new", TenantID: "tnt_1", CreatedAt: cutoff.Add(time.Hour)},
	}
	if err := s.CreateRequestLogs(ctx, records); err != nil {
		t.Fatalf("CreateRequestLogs() error = %v, want nil", err)
	}

	removed, err := s.DeleteRequestLogsBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteRequestLogsBefore() error = %v, want nil", err)
	}
	if removed != 1 {
		t.Fatalf("DeleteRequestLogsBefore() removed %d rows, want 1", removed)
	}
	page, err := s.ListRequestLogs(ctx, store.ListQuery{}, store.RequestLogFilter{TenantID: "tnt_1"})
	if err != nil {
		t.Fatalf("ListRequestLogs() error = %v, want nil", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "req_new" {
		t.Fatalf("ListRequestLogs() after cleanup = %+v, want only req_new to remain", page.Items)
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./service/aiServeWeaveControlPlane/internal/store/memstore/... -run RequestLog -v`
Expected: PASS

- [ ] **Step 5: Confirm the whole control plane still builds now that both store implementations are complete**

Run: `go build ./service/aiServeWeaveControlPlane/...`
Expected: success (the `var _ store.Store = (*Store)(nil)` assertions in both packages now pass)

- [ ] **Step 6: Commit**

```bash
git add service/aiServeWeaveControlPlane/internal/store/memstore/memstore.go \
  service/aiServeWeaveControlPlane/internal/store/memstore/requestlogs.go \
  service/aiServeWeaveControlPlane/internal/store/memstore/requestlogs_test.go
git commit -m "$(cat <<'EOF'
feat(controlplane): implement request_logs persistence in memstore

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: `logic.Service` methods

**Files:**
- Create: `service/aiServeWeaveControlPlane/internal/logic/requestlogs.go`
- Create: `service/aiServeWeaveControlPlane/internal/logic/requestlogs_test.go`

**Interfaces:**
- Consumes: `s.store` (the `store.Store` field already on `logic.Service`, same as `CreateJob` uses), `model.RequestLog`.
- Produces: `logic.CreateRequestLogParams`, `(*Service).CreateRequestLogs(ctx, []CreateRequestLogParams) (accepted int, err error)`, `(*Service).ListRequestLogs(ctx, store.ListQuery, store.RequestLogFilter) (store.Page[model.RequestLog], error)`.

- [ ] **Step 1: Write the failing test**

This package's test convention (confirmed from `jobs_test.go`) is the external test package `logic_test`, a shared `fixture` type built by `newFixture(t)` exposing at least `f.svc *logic.Service` and `f.tenant model.Tenant`. Use that exact fixture rather than inventing a new constructor:

```go
package logic_test

import (
	"context"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

func TestCreateRequestLogsRejectsAMissingRequiredField(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name   string
		params logic.CreateRequestLogParams
	}{
		{name: "missing request id", params: logic.CreateRequestLogParams{TenantID: f.tenant.ID, Endpoint: "chat", Outcome: "ok", CreatedAt: time.Now()}},
		{name: "missing tenant id", params: logic.CreateRequestLogParams{RequestID: "req_1", Endpoint: "chat", Outcome: "ok", CreatedAt: time.Now()}},
		{name: "missing endpoint", params: logic.CreateRequestLogParams{RequestID: "req_1", TenantID: f.tenant.ID, Outcome: "ok", CreatedAt: time.Now()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accepted, err := f.svc.CreateRequestLogs(context.Background(), []logic.CreateRequestLogParams{tt.params})
			if err != nil {
				t.Fatalf("CreateRequestLogs(%+v) error = %v, want nil (an invalid entry is skipped, not an error)", tt.params, err)
			}
			if accepted != 0 {
				t.Fatalf("CreateRequestLogs(%+v) accepted = %d, want 0 (the entry is invalid)", tt.params, accepted)
			}
		})
	}
}

func TestCreateRequestLogsAcceptsAValidBatchAndSkipsInvalidEntries(t *testing.T) {
	f := newFixture(t)
	valid := logic.CreateRequestLogParams{RequestID: "req_ok", TenantID: f.tenant.ID, Endpoint: "chat", StatusCode: 200, Outcome: "ok", DurationMS: 10, CreatedAt: time.Now()}
	invalid := logic.CreateRequestLogParams{Endpoint: "chat"} // missing RequestID/TenantID

	accepted, err := f.svc.CreateRequestLogs(context.Background(), []logic.CreateRequestLogParams{valid, invalid})
	if err != nil {
		t.Fatalf("CreateRequestLogs() error = %v, want nil (a batch with one bad entry still persists the good ones)", err)
	}
	if accepted != 1 {
		t.Fatalf("CreateRequestLogs() accepted = %d, want 1 (only the valid entry)", accepted)
	}

	page, err := f.svc.ListRequestLogs(context.Background(), store.ListQuery{}, store.RequestLogFilter{TenantID: f.tenant.ID})
	if err != nil {
		t.Fatalf("ListRequestLogs() error = %v, want nil", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "req_ok" {
		t.Fatalf("ListRequestLogs() = %+v, want exactly req_ok", page.Items)
	}
}
```

Before finalizing, open `jobs_test.go` to confirm `newFixture`'s exact returned field names (`svc`/`tenant` are the names read during planning; confirm rather than assume, since this fixture is shared, not defined by this task).

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./service/aiServeWeaveControlPlane/internal/logic/... -run RequestLog -v`
Expected: FAIL (compile error — `CreateRequestLogParams`/`CreateRequestLogs`/`ListRequestLogs` do not exist yet)

- [ ] **Step 3: Write the implementation**

```go
// requestlogs.go implements the logic layer for STATUS.md's P09/C28: turning
// a Gateway's batch push into validated model.RequestLog rows, and serving
// the tenant/operator search queries against them.
//
// requestlogs.go 实现 STATUS.md P09/C28 的逻辑层：把 Gateway 的一次批量推送
// 转换为经过校验的 model.RequestLog 行，并服务针对它们的租户/运维检索查询。
package logic

import (
	"context"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreateRequestLogParams is one record a Gateway replica reports.
//
// CreateRequestLogParams 是一条由 Gateway 副本上报的记录。
type CreateRequestLogParams struct {
	RequestID  string
	TenantID   string
	KeyDisplay string
	Endpoint   string
	StatusCode int
	Outcome    string
	DurationMS int64
	CreatedAt  time.Time
}

// CreateRequestLogs persists a batch of records, skipping — not failing on
// — any entry missing a required field. A partially malformed batch from a
// Gateway replica should not cost the well-formed entries in it their
// chance to be searchable; a bug in one caller's payload construction is
// visible in the accepted count, not by losing everything else in the
// batch.
//
// It performs no tenant-scoped authorization: the caller is the Gateway,
// authenticated by the internal shared secret at the handler layer — the
// same trust boundary CreateJob already crosses.
//
// CreateRequestLogs 持久化一批记录，对任何缺失必填字段的条目是跳过而不是
//让整批失败。一个 Gateway 副本发来的部分畸形批次，不应该让其中格式良好的
// 条目失去被检索到的机会；某个调用方载荷构造上的缺陷，体现在被接受的计数
// 里，而不是拖累批次里的其余一切。
//
// 它不做任何按租户的授权检查：调用方是 Gateway，在 handler 层已由内部共享
// 密钥认证——与 CreateJob 已经跨过的是同一条信任边界。
func (s *Service) CreateRequestLogs(ctx context.Context, batch []CreateRequestLogParams) (accepted int, err error) {
	records := make([]model.RequestLog, 0, len(batch))
	for _, p := range batch {
		if p.RequestID == "" || p.TenantID == "" || p.Endpoint == "" {
			continue
		}
		records = append(records, model.RequestLog{
			ID:         p.RequestID,
			TenantID:   p.TenantID,
			KeyDisplay: p.KeyDisplay,
			Endpoint:   p.Endpoint,
			StatusCode: p.StatusCode,
			Outcome:    p.Outcome,
			DurationMS: p.DurationMS,
			CreatedAt:  p.CreatedAt,
		})
	}
	if len(records) == 0 {
		return 0, nil
	}
	if err := s.store.CreateRequestLogs(ctx, records); err != nil {
		return 0, err
	}
	return len(records), nil
}

// ListRequestLogs reads one page of persisted request records. Tenant
// scoping (or its deliberate absence, for the operator cross-tenant search)
// is entirely the caller's responsibility via filter.TenantID — this method
// applies no authorization of its own, matching ListJobs.
//
// ListRequestLogs 读取一页已持久化的请求记录。租户限定(或运维跨租户检索时
// 刻意不限定)完全是调用方通过 filter.TenantID 承担的责任——本方法不做任何
// 自己的授权判断，与 ListJobs 一致。
func (s *Service) ListRequestLogs(ctx context.Context, query store.ListQuery, filter store.RequestLogFilter) (store.Page[model.RequestLog], error) {
	return s.store.ListRequestLogs(ctx, query, filter)
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./service/aiServeWeaveControlPlane/internal/logic/... -run RequestLog -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add service/aiServeWeaveControlPlane/internal/logic/requestlogs.go \
  service/aiServeWeaveControlPlane/internal/logic/requestlogs_test.go
git commit -m "$(cat <<'EOF'
feat(controlplane): add request-log logic layer (create batch, list page)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: HTTP handlers, wire types and route registration

**Files:**
- Modify: `service/aiServeWeaveControlPlane/internal/types/types.go` (add wire types)
- Modify: `service/aiServeWeaveControlPlane/internal/handler/handlers.go` (add three handler functions)
- Modify: `service/aiServeWeaveControlPlane/internal/handler/routes.go` (register three routes)
- Create: `service/aiServeWeaveControlPlane/internal/handler/requestlogs_test.go`

**Interfaces:**
- Consumes: `logic.CreateRequestLogParams`, `(*logic.Service).CreateRequestLogs/ListRequestLogs`, existing helpers `decode`, `writeJSON`, `writeError`, `respondErr`, `actorFrom`, `listQuery`, `timeParam`, `instrumented`, `requireSession`, `requirePlatformSession`, `requireSharedSecret`.
- Produces: `POST /internal/v1/requestlogs`, `GET /admin/v1/requests`, `GET /operator/v1/requests`.

- [ ] **Step 1: Add wire types to `types.go`, near `CreateJobRequest`/`JobHistoryResponse`**

```go
// RequestLogRecordRequest is one record inside a POST /internal/v1/requestlogs
// batch.
type RequestLogRecordRequest struct {
	RequestID  string `json:"request_id"`
	TenantID   string `json:"tenant_id"`
	KeyDisplay string `json:"key_display,omitempty"`
	Endpoint   string `json:"endpoint"`
	StatusCode int    `json:"status_code"`
	Outcome    string `json:"outcome"`
	DurationMS int64  `json:"duration_ms"`
	CreatedAt  time.Time `json:"created_at"`
}

// CreateRequestLogsRequest is the body of POST /internal/v1/requestlogs.
type CreateRequestLogsRequest struct {
	Records []RequestLogRecordRequest `json:"records"`
}

// CreateRequestLogsResponse reports how many of the submitted records were
// accepted, so a Gateway replica's own observability can tell a malformed
// payload from a healthy one without the internal API ever needing to fail
// the whole batch over one bad entry.
type CreateRequestLogsResponse struct {
	Accepted int `json:"accepted"`
}

// RequestLogResponse is one persisted request record, as both the tenant and
// the operator search endpoints render it. It carries every field
// request_logs stores — there is no route-binding-style internal-only data
// to strip here, unlike JobResponse versus JobHistoryResponse.
type RequestLogResponse struct {
	RequestID  string    `json:"request_id"`
	TenantID   string    `json:"tenant_id,omitempty"`
	KeyDisplay string    `json:"key_display"`
	Endpoint   string    `json:"endpoint"`
	StatusCode int       `json:"status_code"`
	Outcome    string    `json:"outcome"`
	DurationMS int64     `json:"duration_ms"`
	CreatedAt  time.Time `json:"created_at"`
}

// RequestLogListResponse is one page of request records.
type RequestLogListResponse struct {
	Items      []RequestLogResponse `json:"items"`
	NextCursor string                `json:"next_cursor,omitempty"`
}
```

- [ ] **Step 2: Add the three handlers to `handlers.go`, near the Job internal-API handlers**

```go
// createRequestLogs handles POST /internal/v1/requestlogs: a Gateway
// replica reports a batch of authenticated front-door requests it just
// finished serving. Like createJob, this call must never sit on an
// inference request's own critical path — that discipline belongs to the
// Gateway's own bounded background pusher, not to this handler, which only
// does the write it is asked to do.
//
// createRequestLogs 处理 POST /internal/v1/requestlogs：一个 Gateway 副本
// 报告一批它刚服务完的、已通过鉴权的前门请求。与 createJob 一样，这次调用
// 绝不能出现在推理请求自己的关键路径上——那份纪律属于 Gateway 自己的有界
// 后台推送器，不属于这个只负责完成被要求的写入的 handler。
func createRequestLogs(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.CreateRequestLogsRequest
		if !decode(w, r, &req) {
			return
		}
		batch := make([]logic.CreateRequestLogParams, len(req.Records))
		for i, rec := range req.Records {
			batch[i] = logic.CreateRequestLogParams{
				RequestID: rec.RequestID, TenantID: rec.TenantID, KeyDisplay: rec.KeyDisplay,
				Endpoint: rec.Endpoint, StatusCode: rec.StatusCode, Outcome: rec.Outcome,
				DurationMS: rec.DurationMS, CreatedAt: rec.CreatedAt,
			}
		}
		accepted, err := ctx.Logic.CreateRequestLogs(r.Context(), batch)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, types.CreateRequestLogsResponse{Accepted: accepted})
	}
}

// renderRequestLog converts a persisted request record to its wire form.
// includeTenant is false on the tenant-scoped endpoint, where the tenant is
// already implied by the caller's own session and repeating it on every row
// would be noise, not information.
//
// renderRequestLog 把一条持久化的请求记录转换成线上形式。includeTenant 在
// 按租户限定的端点上为 false——那里租户已经由调用方自己的会话隐含，在每一行
// 上重复它是噪音，不是信息。
func renderRequestLog(r model.RequestLog, includeTenant bool) types.RequestLogResponse {
	out := types.RequestLogResponse{
		RequestID: r.ID, KeyDisplay: r.KeyDisplay, Endpoint: r.Endpoint,
		StatusCode: r.StatusCode, Outcome: r.Outcome, DurationMS: r.DurationMS, CreatedAt: r.CreatedAt,
	}
	if includeTenant {
		out.TenantID = r.TenantID
	}
	return out
}

// requestLogFilterFrom reads the query parameters both search endpoints
// share.
//
// requestLogFilterFrom 读取两个检索端点共用的查询参数。
func requestLogFilterFrom(query url.Values) (store.RequestLogFilter, bool) {
	since, sinceOK := timeParam(query.Get("since"))
	until, untilOK := timeParam(query.Get("until"))
	if !sinceOK || !untilOK {
		return store.RequestLogFilter{}, false
	}
	return store.RequestLogFilter{
		RequestID: query.Get("request_id"),
		Outcome:   query.Get("status"),
		Since:     since,
		Until:     until,
	}, true
}

// listRequestLogsTenant handles GET /admin/v1/requests, scoped to the
// caller's own tenant — STATUS.md's P09/C28 tenant self-service view.
//
// listRequestLogsTenant 处理 GET /admin/v1/requests，限定在调用方自己的
// 租户范围内——STATUS.md P09/C28 的租户自助视角。
func listRequestLogsTenant(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		filter, ok := requestLogFilterFrom(r.URL.Query())
		if !ok {
			writeError(w, http.StatusBadRequest, "since and until must be RFC 3339 timestamps")
			return
		}
		filter.TenantID = actor.TenantID
		page, err := ctx.Logic.ListRequestLogs(r.Context(), listQuery(r.URL.Query()), filter)
		if err != nil {
			respondErr(w, err)
			return
		}
		out := make([]types.RequestLogResponse, len(page.Items))
		for i, rec := range page.Items {
			out[i] = renderRequestLog(rec, false)
		}
		writeJSON(w, http.StatusOK, types.RequestLogListResponse{Items: out, NextCursor: page.NextCursor})
	}
}

// listRequestLogsOperator handles GET /operator/v1/requests — STATUS.md's
// P09/C28 platform cross-tenant view. An absent tenant_id searches every
// tenant; a present one narrows to it, the same optional-scope shape
// listActiveJobsForRoute already has no equivalent of on the tenant side,
// because only the platform surface is trusted to ask for everything at
// once.
//
// listRequestLogsOperator 处理 GET /operator/v1/requests——STATUS.md
// P09/C28 的平台跨租户视角。缺席的 tenant_id 会检索每一个租户；给出时则
// 收窄到该租户。这种可选范围的形状，租户一侧没有对应物，因为只有平台入口
// 才被信任可以一次性检索全部租户。
func listRequestLogsOperator(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, ok := requestLogFilterFrom(r.URL.Query())
		if !ok {
			writeError(w, http.StatusBadRequest, "since and until must be RFC 3339 timestamps")
			return
		}
		filter.TenantID = r.URL.Query().Get("tenant_id")
		page, err := ctx.Logic.ListRequestLogs(r.Context(), listQuery(r.URL.Query()), filter)
		if err != nil {
			respondErr(w, err)
			return
		}
		out := make([]types.RequestLogResponse, len(page.Items))
		for i, rec := range page.Items {
			out[i] = renderRequestLog(rec, true)
		}
		writeJSON(w, http.StatusOK, types.RequestLogListResponse{Items: out, NextCursor: page.NextCursor})
	}
}
```

Add `"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"` and `"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"` to `handlers.go`'s imports if not already present (check first — `model` and `store` are very likely already imported given `renderJobHistory` and `listQuery` already use them).

- [ ] **Step 3: Register the three routes in `routes.go`**

Add near the existing `/internal/v1/jobs` and `/admin/v1/jobs/history` groups:

```go
	server.AddRoutes([]rest.Route{
		{Method: http.MethodPost, Path: "/internal/v1/requestlogs", Handler: instrumented(ctx.MetricsRegistry, "/internal/v1/requestlogs", requireSharedSecret(ctx.Config.InternalToken, createRequestLogs(ctx)))},
		{Method: http.MethodGet, Path: "/admin/v1/requests", Handler: instrumented(ctx.MetricsRegistry, "/admin/v1/requests", requireSession(ctx, listRequestLogsTenant(ctx)))},
		{Method: http.MethodGet, Path: "/operator/v1/requests", Handler: instrumented(ctx.MetricsRegistry, "/operator/v1/requests", requirePlatformSession(ctx, listRequestLogsOperator(ctx)))},
	})
```

- [ ] **Step 4: Write handler-level tests**

This package's convention (confirmed from `metricshistory_test.go`, the closest existing analog — a read-only, platform-scoped search endpoint) is: internal test package `package handler` (white-box), construct `ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}` with `st := memstore.New()`, and call the handler function **directly** (`someHandler(ctx)(w, req)`), bypassing `requireSession`/`requirePlatformSession`/`requireSharedSecret` entirely — those wrapper functions guard the route table, not the handler bodies, and are exercised by whatever tests already cover them generically (not per-endpoint). Tenant scoping on `listRequestLogsTenant` is exercised by injecting an actor via this package's own `withActor` helper, the same one `middleware.go`'s session guard populates in production.

```go
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

func TestCreateRequestLogsAcceptsAValidEntryAndSkipsAnInvalidOne(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	body := `{"records":[
		{"request_id":"req_ok","tenant_id":"tnt_1","endpoint":"chat","status_code":200,"outcome":"ok","duration_ms":10,"created_at":"2026-09-11T08:00:00Z"},
		{"endpoint":"chat"}
	]}`
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/requestlogs", strings.NewReader(body))
	w := httptest.NewRecorder()

	createRequestLogs(ctx)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp types.CreateRequestLogsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Accepted != 1 {
		t.Fatalf("resp.Accepted = %d, want 1 (only the well-formed record)", resp.Accepted)
	}
}

func TestListRequestLogsTenantScopesToTheCallersTenant(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	now := time.Now()
	if err := st.CreateRequestLogs(t.Context(), []model.RequestLog{
		{ID: "req_mine", TenantID: "tnt_1", Endpoint: "chat", StatusCode: 200, Outcome: "ok", CreatedAt: now},
		{ID: "req_other", TenantID: "tnt_2", Endpoint: "chat", StatusCode: 200, Outcome: "ok", CreatedAt: now},
	}); err != nil {
		t.Fatalf("seeding CreateRequestLogs() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/requests", nil)
	req = req.WithContext(withActor(req.Context(), logic.Actor{TenantID: "tnt_1"}))
	w := httptest.NewRecorder()
	listRequestLogsTenant(ctx)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp types.RequestLogListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].RequestID != "req_mine" {
		t.Fatalf("resp.Items = %+v, want exactly req_mine (tnt_2's row must not leak in)", resp.Items)
	}
	if resp.Items[0].TenantID != "" {
		t.Errorf("resp.Items[0].TenantID = %q, want empty on the tenant-scoped endpoint", resp.Items[0].TenantID)
	}
}

func TestListRequestLogsTenantRejectsAMalformedSince(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/requests?since=not-a-time", nil)
	req = req.WithContext(withActor(req.Context(), logic.Actor{TenantID: "tnt_1"}))
	w := httptest.NewRecorder()
	listRequestLogsTenant(ctx)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a malformed since", w.Code)
	}
}

func TestListRequestLogsOperatorSearchesAcrossTenantsWhenNoneIsGiven(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	now := time.Now()
	if err := st.CreateRequestLogs(t.Context(), []model.RequestLog{
		{ID: "req_a", TenantID: "tnt_a", Endpoint: "chat", StatusCode: 200, Outcome: "ok", CreatedAt: now},
		{ID: "req_b", TenantID: "tnt_b", Endpoint: "chat", StatusCode: 200, Outcome: "ok", CreatedAt: now},
	}); err != nil {
		t.Fatalf("seeding CreateRequestLogs() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/operator/v1/requests", nil)
	w := httptest.NewRecorder()
	listRequestLogsOperator(ctx)(w, req)

	var resp types.RequestLogListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("resp.Items = %+v, want both tenants' rows with no tenant_id filter", resp.Items)
	}
	for _, item := range resp.Items {
		if item.TenantID == "" {
			t.Errorf("item %+v has an empty TenantID, want it populated on the operator endpoint", item)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/operator/v1/requests?tenant_id=tnt_a", nil)
	w = httptest.NewRecorder()
	listRequestLogsOperator(ctx)(w, req)
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].RequestID != "req_a" {
		t.Fatalf("resp.Items = %+v, want exactly req_a when tenant_id=tnt_a", resp.Items)
	}
}
```

Add `"strings"` to this test file's imports for `strings.NewReader`.

- [ ] **Step 5: Run**

Run: `go test ./service/aiServeWeaveControlPlane/internal/handler/... -run RequestLog -v`
Expected: PASS

Run: `go build ./service/aiServeWeaveControlPlane/...`
Expected: success

- [ ] **Step 6: Commit**

```bash
git add service/aiServeWeaveControlPlane/internal/types/types.go \
  service/aiServeWeaveControlPlane/internal/handler/handlers.go \
  service/aiServeWeaveControlPlane/internal/handler/routes.go \
  service/aiServeWeaveControlPlane/internal/handler/requestlogs_test.go
git commit -m "$(cat <<'EOF'
feat(controlplane): expose request-log internal push and search endpoints

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Retention sweeper and `ServiceContext` wiring

**Files:**
- Create: `service/aiServeWeaveControlPlane/internal/requestlogretention/retention.go`
- Create: `service/aiServeWeaveControlPlane/internal/requestlogretention/retention_test.go`
- Modify: `service/aiServeWeaveControlPlane/internal/config/config.go` (add `RequestLogRetention time.Duration` field + validation + default constant)
- Modify: `service/aiServeWeaveControlPlane/internal/svc/servicecontext.go` (start the sweeper unconditionally, tear it down in `Close`)

**Interfaces:**
- Consumes: `store.Store` (specifically `DeleteRequestLogsBefore`), `runtime.Clock`.
- Produces: `requestlogretention.Retention`, `(*Retention).RunOnce/Run`.

- [ ] **Step 1: Write the package, mirroring `metricshistory.Retention` exactly**

```go
// Package requestlogretention periodically deletes request_logs rows past
// their retention window, per STATUS.md's P09/C28. It is a separate,
// narrow package rather than a method tacked onto metricshistory: the two
// tables are unrelated data with unrelated retention policies, and the only
// thing they share is the shape of "delete rows older than a cutoff on a
// timer" — which this package copies rather than abstracts over, since a
// shared abstraction over two unrelated call sites would buy nothing but an
// extra layer of indirection.
//
// requestlogretention 包定时删除超出保留期的 request_logs 行，对应
// STATUS.md 的 P09/C28。它是一个独立的、窄的包，而不是挂在 metricshistory
// 上的一个方法：这两张表是互不相关的数据、互不相关的保留策略，两者唯一的
// 共同点只是"定时删除早于某个截止时间的行"这个形状——本包选择复制这个形状
// 而不是为它抽象出共用逻辑，因为给两个互不相关的调用点做一层共享抽象，除了
// 多一层间接之外什么都买不到。
package requestlogretention

import (
	"context"
	"log/slog"
	"time"

	"AIServeWeave/common/runtime"
)

// Store is the persistence surface retention cleanup needs.
//
// Store 是保留期清理所需要的持久化接口。
type Store interface {
	DeleteRequestLogsBefore(ctx context.Context, before time.Time) (int64, error)
}

// Retention periodically deletes request_logs rows past their retention
// window.
//
// Retention 定时删除超出保留期的 request_logs 行。
type Retention struct {
	store     Store
	retention time.Duration
	clock     runtime.Clock
	logger    *slog.Logger
}

// New builds a Retention. A nil clock defaults to the system clock; a nil
// logger discards.
//
// New 构造一个 Retention。clock 为 nil 时使用系统时钟；logger 为 nil 时
// 丢弃日志。
func New(store Store, retention time.Duration, clock runtime.Clock, logger *slog.Logger) *Retention {
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Retention{store: store, retention: retention, clock: clock, logger: logger}
}

// RunOnce deletes every row older than the retention window, once.
//
// RunOnce 一次性删除全部超出保留期的行。
func (r *Retention) RunOnce(ctx context.Context) error {
	before := r.clock.Now().Add(-r.retention)
	n, err := r.store.DeleteRequestLogsBefore(ctx, before)
	if err != nil {
		return err
	}
	if n > 0 {
		r.logger.Info("request log retention cleanup", slog.Int64("rows_deleted", n), slog.Time("before", before))
	}
	return nil
}

// Run calls RunOnce once per interval until ctx is done.
//
// Run 每隔 interval 调用一次 RunOnce，直到 ctx 结束。
func (r *Retention) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.RunOnce(ctx); err != nil {
				r.logger.Error("request log retention cleanup failed", slog.Any("error", err))
			}
		}
	}
}
```

- [ ] **Step 2: Write the test, injecting a fake clock and a fake store (no real database, no real timer wait)**

This package's convention (confirmed from `metricshistory`'s own `retention_test.go`) is the external test package `requestlogretention_test`, and — there being no shared fake-clock package in this repo — a small local `fakeClock` type defined right in the test file, implementing `runtime.Clock` with only `Now()` needed (`NewTimer` panics if called, since `RunOnce` never calls it):

```go
package requestlogretention_test

import (
	"context"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/requestlogretention"
)

// fakeClock is a minimal runtime.Clock fixed at a known instant — RunOnce
// only ever calls Now(), so NewTimer is never exercised and left unused.
//
// fakeClock 是一个固定在已知时刻的最小 runtime.Clock——RunOnce 只调用 Now()，
// NewTimer 从未被用到，故留空未实现。
type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }
func (c fakeClock) NewTimer(time.Duration) (<-chan time.Time, func() bool) {
	panic("not used by RunOnce")
}

type fakeStore struct {
	deletedBefore time.Time
	toReturn      int64
	err           error
}

func (f *fakeStore) DeleteRequestLogsBefore(_ context.Context, before time.Time) (int64, error) {
	f.deletedBefore = before
	return f.toReturn, f.err
}

func TestRunOnceDeletesRowsOlderThanRetention(t *testing.T) {
	clock := fakeClock{now: time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)}
	store := &fakeStore{toReturn: 3}
	r := requestlogretention.New(store, 30*24*time.Hour, clock, nil)

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v, want nil", err)
	}
	want := clock.now.Add(-30 * 24 * time.Hour)
	if !store.deletedBefore.Equal(want) {
		t.Fatalf("DeleteRequestLogsBefore called with before = %v, want %v", store.deletedBefore, want)
	}
}

func TestRunOnceReturnsTheStoreError(t *testing.T) {
	store := &fakeStore{err: context.DeadlineExceeded}
	r := requestlogretention.New(store, time.Hour, nil, nil)
	if err := r.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce() error = nil, want the store's error to propagate")
	}
}
```

- [ ] **Step 3: Run**

Run: `go test ./service/aiServeWeaveControlPlane/internal/requestlogretention/... -v`
Expected: PASS

- [ ] **Step 4: Add the config field**

In `config.go`, near `MetricsHistory MetricsHistoryConf`:

```go
	// RequestLogRetention is how long a persisted request_logs row (STATUS.md's
	// P09/C28) is kept before the retention sweeper reaps it. Zero uses
	// DefaultRequestLogRetention. Unlike MetricsHistory, this sweeper always
	// runs once the table is migrated — there is no separate "is it
	// configured" question, since the internal push API that populates the
	// table is mounted unconditionally whenever InternalToken is set.
	//
	// RequestLogRetention 是一条已持久化的 request_logs 行(STATUS.md 的
	// P09/C28)在被保留期清理协程回收之前保留多久。为零时采用
	// DefaultRequestLogRetention。与 MetricsHistory 不同，这个清理协程只要
	// 表已迁移就总会运行——不存在一个独立的"是否配置了"的问题，因为填充这张
	// 表的内部推送 API，只要设置了 InternalToken 就无条件挂载。
	RequestLogRetention time.Duration `json:",optional"`
```

Add near the top-level constants (wherever `DefaultArtifactRetention`-style constants for this package live, or directly above the `Config` struct if none exist yet):

```go
// DefaultRequestLogRetention is how long a request_logs row is kept when
// Config.RequestLogRetention is zero.
const DefaultRequestLogRetention = 30 * 24 * time.Hour
```

Add a validation guard alongside `MetricsHistory.Interval`'s check in `Validate()`:

```go
	if c.RequestLogRetention < 0 {
		return errors.New("config: RequestLogRetention must not be negative")
	}
```

- [ ] **Step 5: Wire the sweeper into `ServiceContext`**

In `servicecontext.go`, add fields next to `metricsHistoryCancel`/`metricsHistoryDone`:

```go
	requestLogRetentionCancel context.CancelFunc
	requestLogRetentionDone   chan struct{}
```

After the `MetricsHistory.Enabled()` block in `NewServiceContext` (this one runs unconditionally, no `if`):

```go
	requestLogRetention := cfg.RequestLogRetention
	if requestLogRetention <= 0 {
		requestLogRetention = config.DefaultRequestLogRetention
	}
	rlCtx, rlCancel := context.WithCancel(ctx)
	rlDone := make(chan struct{})
	go func() {
		defer close(rlDone)
		requestlogretention.New(st, requestLogRetention, nil, nil).Run(rlCtx, requestLogRetentionInterval)
	}()
```

Add the import `"AIServeWeave/service/aiServeWeaveControlPlane/internal/requestlogretention"`.

Add the constant next to `metricsHistoryRetentionInterval`:

```go
// requestLogRetentionInterval is how often the request-log retention
// cleanup goroutine runs — daily, the same cadence metricsHistoryRetentionInterval
// already uses, for the same reason: a cleanup task has no reason to run
// more often than once a day.
const requestLogRetentionInterval = 24 * time.Hour
```

Include the two new fields in the `&ServiceContext{...}` literal (`requestLogRetentionCancel: rlCancel, requestLogRetentionDone: rlDone,`), and in `Close()`, mirroring the `metricsHistoryCancel` block:

```go
	if s.requestLogRetentionCancel != nil {
		s.requestLogRetentionCancel()
		<-s.requestLogRetentionDone
	}
```

- [ ] **Step 6: Confirm the control plane builds and existing config tests still pass**

Run: `go build ./service/aiServeWeaveControlPlane/...`
Run: `go test ./service/aiServeWeaveControlPlane/internal/config/... -v`
Expected: both succeed

- [ ] **Step 7: Commit**

```bash
git add service/aiServeWeaveControlPlane/internal/requestlogretention \
  service/aiServeWeaveControlPlane/internal/config/config.go \
  service/aiServeWeaveControlPlane/internal/svc/servicecontext.go
git commit -m "$(cat <<'EOF'
feat(controlplane): add request_logs retention sweeper

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: Gateway — outcome mapping, endpoint classification and the record type (pure logic)

**Files:**
- Create: `service/aiServeWeaveGateway/httpapi/requestlog.go`
- Create: `service/aiServeWeaveGateway/httpapi/requestlog_test.go`

**Interfaces:**
- Produces: `type requestLogRecord struct{...}`, `func outcomeForStatus(status int) string`, closed `Outcome*` constants, `func requestLogEndpoint(path string) (string, bool)`.

- [ ] **Step 1: Write the failing tests**

```go
package httpapi

import "testing"

func TestOutcomeForStatusIsAClosedMapping(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   string
	}{
		{name: "200 is ok", status: 200, want: OutcomeOK},
		{name: "201 is ok", status: 201, want: OutcomeOK},
		{name: "400 is invalid_request", status: 400, want: OutcomeInvalidRequest},
		{name: "401 is unauthorized", status: 401, want: OutcomeUnauthorized},
		{name: "403 is forbidden", status: 403, want: OutcomeForbidden},
		{name: "404 is not_found", status: 404, want: OutcomeNotFound},
		{name: "429 is rate_limited", status: 429, want: OutcomeRateLimited},
		{name: "500 is internal", status: 500, want: OutcomeInternal},
		{name: "502 is upstream_unavailable", status: 502, want: OutcomeUpstreamUnavailable},
		{name: "503 is upstream_unavailable", status: 503, want: OutcomeUpstreamUnavailable},
		{name: "504 is upstream_unavailable", status: 504, want: OutcomeUpstreamUnavailable},
		{name: "an unrecognized status falls back to error", status: 418, want: OutcomeError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := outcomeForStatus(tt.status); got != tt.want {
				t.Fatalf("outcomeForStatus(%d) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestRequestLogEndpointOnlyMatchesTheFourFrontDoorRoutes(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		wantEP   string
		wantOK   bool
	}{
		{name: "models", path: "/v1/models", wantEP: "models", wantOK: true},
		{name: "chat completions", path: "/v1/chat/completions", wantEP: "chat", wantOK: true},
		{name: "embeddings", path: "/v1/embeddings", wantEP: "embeddings", wantOK: true},
		{name: "responses", path: "/v1/responses", wantEP: "responses", wantOK: true},
		{name: "job status is out of scope", path: "/v1/jobs/job_1", wantOK: false},
		{name: "workflow run submission is out of scope", path: "/v1/workflows/wf_1/runs", wantOK: false},
		{name: "an unrecognized path is out of scope", path: "/wp-admin.php", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotEP, gotOK := requestLogEndpoint(tt.path)
			if gotOK != tt.wantOK || (gotOK && gotEP != tt.wantEP) {
				t.Fatalf("requestLogEndpoint(%q) = (%q, %v), want (%q, %v)", tt.path, gotEP, gotOK, tt.wantEP, tt.wantOK)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./service/aiServeWeaveGateway/httpapi/... -run "OutcomeForStatus|RequestLogEndpoint" -v`
Expected: FAIL (compile error)

- [ ] **Step 3: Write the implementation**

```go
// requestlog.go is the Gateway's side of STATUS.md's P09/C28: it classifies
// a finished, authenticated front-door request into the closed vocabulary
// the design doc requires, and defines the record shape the background
// pusher (requestlogpush.go) batches to the control plane. Nothing in this
// file reads a request body, a response body, or any free-text error — the
// only inputs are the request path and the final HTTP status code, both of
// which are already public in access logs and carry nothing that needs
// redaction.
//
// requestlog.go 是 Gateway 一侧对 STATUS.md P09/C28 的实现：把一次已完成、
// 已鉴权的前门请求，分类进设计文档要求的封闭词汇表，并定义后台推送器
// (requestlogpush.go)批量发往控制面所用的记录形状。本文件从不读取请求体、
// 响应体或任何自由文本错误——唯一的输入是请求路径与最终 HTTP 状态码，两者
// 本就出现在访问日志里，不携带任何需要脱敏的内容。
package httpapi

import (
	"time"
)

// Outcome values. Closed by design: STATUS.md's P09/C28 requires that no
// free-text error ever reaches the searchable request_logs table, so every
// possible status code must land in one of these, never in the status
// code's own message text.
//
// Outcome 取值。设计上是封闭的：STATUS.md 的 P09/C28 要求任何自由文本错误都
// 不得进入可检索的 request_logs 表，因此每一个可能的状态码都必须落进这些
// 取值之一，而绝不是状态码自身的消息文本。
const (
	OutcomeOK                 = "ok"
	OutcomeInvalidRequest     = "invalid_request"
	OutcomeUnauthorized       = "unauthorized"
	OutcomeForbidden          = "forbidden"
	OutcomeNotFound           = "not_found"
	OutcomeRateLimited        = "rate_limited"
	OutcomeInternal           = "internal"
	OutcomeUpstreamUnavailable = "upstream_unavailable"
	OutcomeError              = "error"
)

// requestLogEndpoint values, the closed set request_logs.endpoint accepts —
// a narrower vocabulary than httpapi's own endpointFor, which also covers
// job-related routes that STATUS.md's P09/C28 explicitly excludes (they
// already have J07's persisted history).
//
// requestLogEndpoint 取值，是 request_logs.endpoint 所接受的封闭集合——比
// httpapi 自己的 endpointFor 更窄，后者还覆盖了 STATUS.md P09/C28 明确排除
// 的 job 相关路由(它们已经有 J07 的持久化历史)。
const (
	requestLogEndpointModels     = "models"
	requestLogEndpointChat       = "chat"
	requestLogEndpointEmbeddings = "embeddings"
	requestLogEndpointResponses  = "responses"
)

// outcomeForStatus maps an HTTP status code onto the closed Outcome
// vocabulary. It is a pure function of the status code alone, precisely so
// that no business handler (chat.go, responses.go, embeddings.go, models.go)
// needs to change to support request search — see the design doc's
// "零改动业务 handler" decision.
//
// outcomeForStatus 把一个 HTTP 状态码映射到封闭的 Outcome 词汇表上。它是一个
// 只依赖状态码本身的纯函数，这正是为了让任何业务 handler(chat.go、
// responses.go、embeddings.go、models.go)都无需为支持请求检索而改动——见
// 设计文档"零改动业务 handler"的决定。
func outcomeForStatus(status int) string {
	switch {
	case status >= 200 && status < 300:
		return OutcomeOK
	case status == 400:
		return OutcomeInvalidRequest
	case status == 401:
		return OutcomeUnauthorized
	case status == 403:
		return OutcomeForbidden
	case status == 404:
		return OutcomeNotFound
	case status == 429:
		return OutcomeRateLimited
	case status == 500:
		return OutcomeInternal
	case status == 502 || status == 503 || status == 504:
		return OutcomeUpstreamUnavailable
	default:
		return OutcomeError
	}
}

// requestLogEndpoint reports the closed endpoint value for path, and
// whether path is one of the four routes STATUS.md's P09/C28 covers at all.
// Every other path — job routes, artifact routes, anything unrecognized —
// returns ok=false, which is the middleware's signal to record nothing.
//
// requestLogEndpoint 报告 path 对应的封闭 endpoint 取值，以及 path 是否属于
// STATUS.md P09/C28 覆盖的四条路由之一。其余任何路径——job 路由、产物路由、
// 任何无法识别的路径——都返回 ok=false，这是中间件"不记录"的信号。
func requestLogEndpoint(path string) (string, bool) {
	switch path {
	case "/v1/models":
		return requestLogEndpointModels, true
	case "/v1/chat/completions":
		return requestLogEndpointChat, true
	case "/v1/embeddings":
		return requestLogEndpointEmbeddings, true
	case "/v1/responses":
		return requestLogEndpointResponses, true
	default:
		return "", false
	}
}

// requestLogRecord is one finished, authenticated front-door request,
// ready to be pushed to the control plane. It carries nothing beyond what
// the design doc's field table allows — no request body, no response body,
// no model name.
//
// requestLogRecord 是一条已完成、已鉴权的前门请求，可供推送至控制面。它携带
// 的字段不超出设计文档字段表所允许的范围——没有请求体、没有响应体、没有
// 模型名。
type requestLogRecord struct {
	RequestID  string
	TenantID   string
	KeyDisplay string
	Endpoint   string
	StatusCode int
	Outcome    string
	DurationMS int64
	CreatedAt  time.Time
}

```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./service/aiServeWeaveGateway/httpapi/... -run "OutcomeForStatus|RequestLogEndpoint" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add service/aiServeWeaveGateway/httpapi/requestlog.go service/aiServeWeaveGateway/httpapi/requestlog_test.go
git commit -m "$(cat <<'EOF'
feat(gateway): add request-log outcome classification and record type

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: Gateway — the request-logging middleware

**Files:**
- Modify: `service/aiServeWeaveGateway/httpapi/requestlog.go` (add the middleware; remove the Task 8 placeholder import guard)
- Modify: `service/aiServeWeaveGateway/httpapi/requestlog_test.go` (add middleware tests)
- Modify: `service/aiServeWeaveGateway/httpapi/metrics.go` (add the dropped-record counter)

**Interfaces:**
- Consumes: `IdentityFrom(ctx)` (`auth.go`), `bearerToken` (`auth.go`, same package), `apikey.Display` (`common/apikey`), `statusWriter` (`httpapi.go`), `requestIDFrom(ctx)` (`context.go`), `runtime.Clock`.
- Produces: `(h *handlers) requestLogMiddleware(next http.Handler) http.Handler`, a `requestLogSink` interface the middleware enqueues onto (implemented by the pusher in Task 10), `recorder.RequestLogDropped()`.

- [ ] **Step 1: Add the metric**

In `metrics.go`, add a new metric name next to `MetricLimiterUnavailableTotal`:

```go
	// MetricRequestLogDroppedTotal counts request-log records dropped
	// because the bounded push buffer was full (STATUS.md's P09/C28). A
	// non-zero slope means the search table is silently missing recent
	// requests, not that anything about serving them failed.
	//
	// MetricRequestLogDroppedTotal 统计因有界推送缓冲已满而被丢弃的请求记录
	// (STATUS.md 的 P09/C28)。斜率非零意味着检索表正在悄悄丢失最近的请求，
	// 而不是服务这些请求本身出了问题。
	MetricRequestLogDroppedTotal = "gateway_request_log_dropped_total"
```

Add it to `Descriptions()`:

```go
		MetricRequestLogDroppedTotal: {
			Kind: metrics.KindCounter,
			Help: "Request-log records dropped because the push buffer was full.",
		},
```

Add a `recorder` method next to `LimiterUnavailable`:

```go
// RequestLogDropped records one request-log record dropped because the
// push buffer was full.
//
// RequestLogDropped 记录一条因推送缓冲已满而被丢弃的请求记录。
func (r *recorder) RequestLogDropped() {
	r.sink.Counter(MetricRequestLogDroppedTotal, nil).Add(1)
}
```

- [ ] **Step 2: Write the failing middleware tests**

Append to `requestlog_test.go`:

```go
type fakeRequestLogSink struct {
	records []requestLogRecord
}

func (s *fakeRequestLogSink) enqueue(r requestLogRecord) bool {
	s.records = append(s.records, r)
	return true
}

func TestRequestLogMiddlewareRecordsOnlyAuthenticatedFrontDoorRequests(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		withIdentity bool
		wantRecorded bool
	}{
		{name: "an authenticated chat request is recorded", path: "/v1/chat/completions", withIdentity: true, wantRecorded: true},
		{name: "no identity in context means no tenant, so nothing is recorded", path: "/v1/chat/completions", withIdentity: false, wantRecorded: false},
		{name: "an authenticated job route is out of scope", path: "/v1/jobs/job_1", withIdentity: true, wantRecorded: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &fakeRequestLogSink{}
			h := &handlers{requestLogs: sink, metrics: newRecorder(nil), clock: runtime.NewSystemClock()}
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			req.Header.Set("Authorization", "Bearer aisw-testkeyplaintextvalue")
			ctx := req.Context()
			if tt.withIdentity {
				ctx = context.WithValue(ctx, identityKey{}, Identity{TenantID: "tnt_1", KeyID: "key_1"})
			}
			rec := httptest.NewRecorder()
			h.requestLogMiddleware(next).ServeHTTP(rec, req.WithContext(ctx))

			if got := len(sink.records) == 1; got != tt.wantRecorded {
				t.Fatalf("recorded a request = %v (records=%v), want %v", got, sink.records, tt.wantRecorded)
			}
		})
	}
}

func TestRequestLogMiddlewareFieldsMatchTheRequest(t *testing.T) {
	sink := &fakeRequestLogSink{}
	h := &handlers{requestLogs: sink, metrics: newRecorder(nil), clock: runtime.NewSystemClock()}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTooManyRequests) })

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", nil)
	req.Header.Set("Authorization", "Bearer aisw-abcdefgh12345678")
	ctx := context.WithValue(req.Context(), identityKey{}, Identity{TenantID: "tnt_9", KeyID: "key_9"})
	rec := httptest.NewRecorder()
	h.requestLogMiddleware(next).ServeHTTP(rec, req.WithContext(ctx))

	if len(sink.records) != 1 {
		t.Fatalf("got %d records, want 1", len(sink.records))
	}
	got := sink.records[0]
	if got.TenantID != "tnt_9" {
		t.Errorf("TenantID = %q, want tnt_9", got.TenantID)
	}
	if got.Endpoint != requestLogEndpointEmbeddings {
		t.Errorf("Endpoint = %q, want %q", got.Endpoint, requestLogEndpointEmbeddings)
	}
	if got.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want %d", got.StatusCode, http.StatusTooManyRequests)
	}
	if got.Outcome != OutcomeRateLimited {
		t.Errorf("Outcome = %q, want %q", got.Outcome, OutcomeRateLimited)
	}
	if got.KeyDisplay == "" || got.KeyDisplay == "aisw-abcdefgh12345678" {
		t.Errorf("KeyDisplay = %q, want a non-empty display form that is not the full plaintext key", got.KeyDisplay)
	}
}

func TestRequestLogMiddlewareIsANoOpWhenNoSinkIsConfigured(t *testing.T) {
	h := &handlers{requestLogs: nil, metrics: newRecorder(nil), clock: runtime.NewSystemClock()}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx := context.WithValue(req.Context(), identityKey{}, Identity{TenantID: "tnt_1"})
	rec := httptest.NewRecorder()
	// Must not panic with a nil sink — this is the "no control plane
	// configured" degrade path every other background feature in this
	// package already follows.
	h.requestLogMiddleware(next).ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the wrapped handler must still run)", rec.Code)
	}
}
```

Add `"context"`, `"net/http/httptest"` and `"AIServeWeave/common/runtime"` to the test file's imports.

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./service/aiServeWeaveGateway/httpapi/... -run RequestLogMiddleware -v`
Expected: FAIL (compile error — `handlers.requestLogs` field and `requestLogMiddleware` method do not exist yet)

- [ ] **Step 4: Write the implementation**

Add to `requestlog.go` (replacing the Task 8 placeholder guard, and removing the now-unneeded blank import line):

```go
// requestLogSink is what the middleware hands a finished record to. It is
// satisfied by the bounded background pusher (requestlogpush.go); tests use
// a fake. enqueue reports whether the record was accepted, purely so the
// middleware's own tests can observe the outcome — the middleware itself
// never acts differently on false, since a dropped record is the sink's own
// bounded-buffer policy, not something the middleware retries or escalates.
//
// requestLogSink 是中间件把一条完成的记录交付给的对象。它由有界后台推送器
// (requestlogpush.go)实现；测试中用假实现替代。enqueue 报告该记录是否被
// 接受，纯粹是为了让中间件自己的测试能够观察结果——中间件本身从不因 false
// 而采取不同行动，因为一条记录被丢弃是接收端自己的有界缓冲策略，不是中间件
// 需要重试或上报的事情。
type requestLogSink interface {
	enqueue(requestLogRecord) bool
}

// requestLogMiddleware records one requestLogRecord per finished request
// that both resolved a tenant identity (auth.middleware already ran) and
// matches one of the four routes STATUS.md's P09/C28 covers. It is placed
// after auth.middleware in the chain specifically so IdentityFrom(ctx) is
// already populated when this code runs — see the design doc's "采集链路"
// section for why that ordering avoids any cross-middleware context-sharing
// machinery.
//
// A nil h.requestLogs (no control plane configured to push to) makes this
// middleware a pure pass-through, the same nil-degrades convention every
// other background feature in this package already follows.
//
// requestLogMiddleware 为每一个既解析出了租户身份(auth.middleware 已经跑过)
// 又匹配 STATUS.md P09/C28 覆盖的四条路由之一的、已完成的请求，记录一条
// requestLogRecord。它被特意放在链路中 auth.middleware 之后，好让这段代码
// 运行时 IdentityFrom(ctx) 已经就绪——为什么这个顺序能避免任何跨中间件的
// context 共享机制，见设计文档「采集链路」一节。
//
// h.requestLogs 为 nil(未配置可供推送的控制面)时，本中间件是纯粹的透传，
// 与本包其余每一个后台特性已经遵循的同一种"为 nil 时退化"约定相同。
func (h *handlers) requestLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.requestLogs == nil {
			next.ServeHTTP(w, r)
			return
		}
		endpoint, ok := requestLogEndpoint(r.URL.Path)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		identity, ok := IdentityFrom(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}

		start := h.clock.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		status := statusOf(sw)

		var keyDisplay string
		if key, ok := bearerToken(r.Header.Get("Authorization")); ok {
			keyDisplay = apikey.Display(key)
		}
		accepted := h.requestLogs.enqueue(requestLogRecord{
			RequestID:  requestIDFrom(r.Context()),
			TenantID:   identity.TenantID,
			KeyDisplay: keyDisplay,
			Endpoint:   endpoint,
			StatusCode: status,
			Outcome:    outcomeForStatus(status),
			DurationMS: h.clock.Now().Sub(start).Milliseconds(),
			CreatedAt:  start,
		})
		if !accepted {
			h.metrics.RequestLogDropped()
		}
	})
}
```

Add `"AIServeWeave/common/apikey"` to `requestlog.go`'s imports. Add a `requestLogs requestLogSink` field to the `handlers` struct in `httpapi.go` (next to `persister`), documented the same nil-degrades way as `persister`.

- [ ] **Step 5: Run to verify it passes**

Run: `go test ./service/aiServeWeaveGateway/httpapi/... -run "RequestLog" -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add service/aiServeWeaveGateway/httpapi/requestlog.go \
  service/aiServeWeaveGateway/httpapi/requestlog_test.go \
  service/aiServeWeaveGateway/httpapi/metrics.go \
  service/aiServeWeaveGateway/httpapi/httpapi.go
git commit -m "$(cat <<'EOF'
feat(gateway): add the request-log middleware

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: Gateway — bounded buffer and background batch pusher

**Files:**
- Create: `service/aiServeWeaveGateway/httpapi/requestlogpush.go`
- Create: `service/aiServeWeaveGateway/httpapi/requestlogpush_test.go`

**Interfaces:**
- Consumes: `requestLogRecord`, `runtime.Clock`, a `RequestLogClient` interface this task defines.
- Produces: `type RequestLogClient interface { PushRequestLogs(ctx, []requestLogRecord) error }`, `type requestLogPusher struct{...}` implementing `requestLogSink`, `newRequestLogPusher(...)`, `(*requestLogPusher).enqueue/run/Stop`, `requestLogPushConfig`, `DefaultRequestLogBufferSize/BatchSize/FlushInterval`.

- [ ] **Step 1: Write the failing tests**

Read `service/aiServeWeaveGateway/httpapi/jobsync_test.go` first for this package's exact fake-`runtime.Clock` test double and the `run()`/`Stop()` lifecycle test idiom (start the goroutine, advance the fake clock, assert, then `Stop()` and confirm no goroutine leak — the leak assertion itself lives in `main_test.go`'s `TestMain`, per AGENTS.md, so this test file does not need its own leak check), then write:

```go
package httpapi

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakePushClient struct {
	mu    sync.Mutex
	calls [][]requestLogRecord
	err   error
}

func (c *fakePushClient) PushRequestLogs(_ context.Context, records []requestLogRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, records)
	return c.err
}

func (c *fakePushClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

func TestRequestLogPusherFlushesOnBatchSize(t *testing.T) {
	client := &fakePushClient{}
	p := newRequestLogPusher(client, nil, nil, requestLogPushConfig{BufferSize: 100, BatchSize: 2, FlushInterval: time.Hour})
	go p.run()
	defer p.Stop()

	p.enqueue(requestLogRecord{RequestID: "req_1"})
	p.enqueue(requestLogRecord{RequestID: "req_2"})

	deadline := time.After(2 * time.Second)
	for client.callCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("PushRequestLogs was never called after the batch size was reached")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestRequestLogPusherDropsWhenBufferIsFull(t *testing.T) {
	client := &fakePushClient{}
	// No run() goroutine draining it, so the buffer fills immediately.
	p := newRequestLogPusher(client, nil, nil, requestLogPushConfig{BufferSize: 1, BatchSize: 10, FlushInterval: time.Hour})

	if ok := p.enqueue(requestLogRecord{RequestID: "req_1"}); !ok {
		t.Fatal("enqueue() = false on the first record, want true (buffer has room)")
	}
	if ok := p.enqueue(requestLogRecord{RequestID: "req_2"}); ok {
		t.Fatal("enqueue() = true on the second record, want false (buffer is full and must drop, not block)")
	}
}

func TestRequestLogPusherStopFlushesWhatItCanAndReturns(t *testing.T) {
	client := &fakePushClient{}
	p := newRequestLogPusher(client, nil, nil, requestLogPushConfig{BufferSize: 100, BatchSize: 100, FlushInterval: time.Hour})
	go p.run()
	p.enqueue(requestLogRecord{RequestID: "req_1"})

	done := make(chan struct{})
	go func() { p.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return; the run loop likely did not exit")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./service/aiServeWeaveGateway/httpapi/... -run RequestLogPusher -v`
Expected: FAIL (compile error)

- [ ] **Step 3: Write the implementation**

```go
// requestlogpush.go is the Gateway's bounded, asynchronous side of
// STATUS.md's P09/C28: it buffers requestLogRecord values enqueued by
// requestlog.go's middleware and pushes them to the control plane in
// batches, on a timer or once a batch fills, whichever comes first. It
// never blocks the middleware and never retries a failed push — both are
// deliberate per the design doc: these are diagnostic records, not job
// state, and a database hiccup here must not become inference-path latency
// or an unbounded local backlog.
//
// requestlogpush.go 是 Gateway 一侧对 STATUS.md P09/C28 有界、异步的那一半：
// 它缓冲 requestlog.go 中间件入队的 requestLogRecord，按定时器或攒满一批
// (以先到者为准)批量推送给控制面。它从不阻塞中间件，也从不重试失败的推送——
// 两者都是设计文档里刻意的决定：这些是诊断性记录，不是 job 状态，这里的一次
// 数据库故障不能变成推理路径的延迟，也不能变成一份无界的本地积压。
package httpapi

import (
	"context"
	"log/slog"
	"time"

	"AIServeWeave/common/runtime"
)

// RequestLogClient pushes a batch of request-log records to the control
// plane. It is declared here, where it is used, and implemented in
// controlplaneclient — the same split KeyVerifier and JobPersistClient
// already use, so this package stays testable without a control plane.
//
// RequestLogClient 把一批请求记录推送给控制面。它声明在使用它的这里，实现在
// controlplaneclient——与 KeyVerifier、JobPersistClient 已经采用的是同一种
// 拆分，好让本包无需控制面即可测试。
type RequestLogClient interface {
	PushRequestLogs(ctx context.Context, records []requestLogRecord) error
}

// requestLogPushConfig tunes the pusher. Zero fields take the package
// defaults below.
//
// requestLogPushConfig 调整推送器的参数。零值字段采用下面的包默认值。
type requestLogPushConfig struct {
	BufferSize    int
	BatchSize     int
	FlushInterval time.Duration
	CallTimeout   time.Duration
}

// Package defaults for requestLogPushConfig, chosen to keep the buffer
// bounded (STATUS.md's AGENTS.md security-line "任何一跳都不得无界缓冲")
// while batching enough that a busy replica does not call the control
// plane once per request.
//
// requestLogPushConfig 的包默认值，选定的目标是让缓冲保持有界(AGENTS.md 安全
// 红线"任何一跳都不得无界缓冲")，同时攒够一批，使一个繁忙的副本不至于每个
// 请求都调用一次控制面。
const (
	DefaultRequestLogBufferSize    = 10000
	DefaultRequestLogBatchSize     = 500
	DefaultRequestLogFlushInterval = 5 * time.Second
	DefaultRequestLogCallTimeout   = 3 * time.Second
)

// requestLogPusher implements requestLogSink.
//
// requestLogPusher 实现 requestLogSink。
type requestLogPusher struct {
	client RequestLogClient
	clock  runtime.Clock
	logger *slog.Logger
	cfg    requestLogPushConfig

	ch   chan requestLogRecord
	stop chan struct{}
	done chan struct{}
}

// newRequestLogPusher builds a pusher with cfg's zero fields replaced by
// defaults. It does not start the background loop; call run in a goroutine
// for that — the same two-step construction jobSyncer already uses.
//
// newRequestLogPusher 用默认值填补 cfg 里的零值字段来构建一个推送器。它不会
// 启动后台循环，要启动需要以协程方式调用 run——与 jobSyncer 相同的两步构造。
func newRequestLogPusher(client RequestLogClient, clock runtime.Clock, logger *slog.Logger, cfg requestLogPushConfig) *requestLogPusher {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = DefaultRequestLogBufferSize
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultRequestLogBatchSize
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = DefaultRequestLogFlushInterval
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = DefaultRequestLogCallTimeout
	}
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &requestLogPusher{
		client: client, clock: clock, logger: logger, cfg: cfg,
		ch:   make(chan requestLogRecord, cfg.BufferSize),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
}

// enqueue implements requestLogSink. It never blocks: a full buffer drops
// the new record and reports false, which the caller (requestlog.go's
// middleware) turns into a metric rather than a retry — see this file's
// package doc comment for why blocking or retrying here is exactly what
// must not happen.
//
// enqueue 实现 requestLogSink。它从不阻塞：缓冲已满时丢弃新记录并报告
// false，调用方(requestlog.go 的中间件)把它变成一次指标而不是一次重试——为
// 什么在这里阻塞或重试正是不该发生的事，见本文件的包文档注释。
func (p *requestLogPusher) enqueue(r requestLogRecord) bool {
	select {
	case p.ch <- r:
		return true
	default:
		return false
	}
}

// run drains the buffer, flushing a batch to the control plane either when
// BatchSize records have accumulated or FlushInterval has elapsed since the
// last flush, whichever comes first. It is meant to be started as
// `go pusher.run()`.
//
// run 消费缓冲，在攒够 BatchSize 条记录或距上次 flush 已过 FlushInterval——
// 以先到者为准——时向控制面 flush 一批。它应当以 `go pusher.run()` 的方式
// 启动。
func (p *requestLogPusher) run() {
	defer close(p.done)
	ticker, stopTicker := p.clock.NewTimer(p.cfg.FlushInterval)
	defer stopTicker()

	batch := make([]requestLogRecord, 0, p.cfg.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), p.cfg.CallTimeout)
		if err := p.client.PushRequestLogs(ctx, batch); err != nil {
			p.logger.Warn("pushing request logs to the control plane failed; the batch is dropped, not retried",
				slog.Any("error", err), slog.Int("batch_size", len(batch)))
		}
		cancel()
		batch = make([]requestLogRecord, 0, p.cfg.BatchSize)
	}

	for {
		select {
		case <-p.stop:
			flush()
			return
		case r := <-p.ch:
			batch = append(batch, r)
			if len(batch) >= p.cfg.BatchSize {
				flush()
			}
		case <-ticker:
			flush()
			ticker, stopTicker = p.clock.NewTimer(p.cfg.FlushInterval)
		}
	}
}

// Stop signals run to flush whatever it currently holds and exit, and
// waits for it to do so.
//
// Stop 通知 run flush 掉当前持有的内容并退出，并等待其完成。
func (p *requestLogPusher) Stop() {
	close(p.stop)
	<-p.done
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./service/aiServeWeaveGateway/httpapi/... -run RequestLogPusher -v -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add service/aiServeWeaveGateway/httpapi/requestlogpush.go service/aiServeWeaveGateway/httpapi/requestlogpush_test.go
git commit -m "$(cat <<'EOF'
feat(gateway): add the bounded request-log background pusher

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: Gateway — `controlplaneclient.RequestLogsClient`, `httpapi.Config` wiring, `main.go`

**Files:**
- Create: `service/aiServeWeaveGateway/controlplaneclient/requestlogs.go`
- Create: `service/aiServeWeaveGateway/controlplaneclient/requestlogs_test.go`
- Modify: `service/aiServeWeaveGateway/httpapi/httpapi.go` (Config fields, wiring in `New`, `Server` struct, `Close`)
- Modify: `service/aiServeWeaveGateway/main.go` (flags/adapter, wiring into `httpCfg`)

**Interfaces:**
- Consumes: `httpapi.RequestLogClient`, `httpapi.requestLogRecord` — note `requestLogRecord` is unexported, so the client package must accept the values through the interface's parameter type; since `PushRequestLogs(ctx, records []requestLogRecord) error` uses an unexported type from `httpapi` in its signature, `controlplaneclient.RequestLogsClient` cannot implement `httpapi.RequestLogClient` directly across the package boundary as written. **Resolve this before writing code**: export the record type from `httpapi` as `RequestLogRecord` (capital R) instead of keeping it unexported — revisit Task 8's `requestLogRecord` and Task 9/10's references, renaming the type (not its fields, which are already exported) to `RequestLogRecord` throughout `requestlog.go`, `requestlog_test.go`, `requestlogpush.go`, and `requestlogpush_test.go` before proceeding. This is a naming correction caught here, not a redesign — do it as this task's Step 0.

**Step 0: Rename `requestLogRecord` to `RequestLogRecord` across Task 8–10 files**

Run: `grep -rl 'requestLogRecord' service/aiServeWeaveGateway/httpapi/ | xargs sed -i '' 's/requestLogRecord/RequestLogRecord/g'`
Run: `go build ./service/aiServeWeaveGateway/... && go test ./service/aiServeWeaveGateway/httpapi/... -run "RequestLog" -v`
Expected: still PASS, purely mechanical rename, and add a one-line doc comment onto the type explaining it is exported specifically so `controlplaneclient.RequestLogClient` implementations outside this package can be typed against it.

- [ ] **Step 1: Write the failing client test**

Read `service/aiServeWeaveGateway/controlplaneclient/jobs_test.go` first for this package's `httptest.Server`-based call-mocking idiom, then write in `requestlogs_test.go`:

```go
package controlplaneclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

func TestPushRequestLogsPostsTheBatchAndAuthenticates(t *testing.T) {
	var gotAuth string
	var gotBody struct {
		Records []struct {
			RequestID string `json:"request_id"`
		} `json:"records"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/internal/v1/requestlogs" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s, want POST /internal/v1/requestlogs", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]int{"accepted": 1})
	}))
	defer server.Close()

	client, err := NewRequestLogsClient(RequestLogsClientConfig{Endpoint: server.URL, Token: "internal-secret"})
	if err != nil {
		t.Fatalf("NewRequestLogsClient() error = %v, want nil", err)
	}
	err = client.PushRequestLogs(context.Background(), []httpapi.RequestLogRecord{
		{RequestID: "req_1", TenantID: "tnt_1", Endpoint: "chat", StatusCode: 200, Outcome: "ok", DurationMS: 5, CreatedAt: time.Now()},
	})
	if err != nil {
		t.Fatalf("PushRequestLogs() error = %v, want nil", err)
	}
	if gotAuth != "Bearer internal-secret" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer internal-secret")
	}
	if len(gotBody.Records) != 1 || gotBody.Records[0].RequestID != "req_1" {
		t.Errorf("posted body records = %+v, want one record with request_id req_1", gotBody.Records)
	}
}

func TestPushRequestLogsOfAnEmptyBatchDoesNotCallTheServer(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer server.Close()
	client, _ := NewRequestLogsClient(RequestLogsClientConfig{Endpoint: server.URL, Token: "t"})
	if err := client.PushRequestLogs(context.Background(), nil); err != nil {
		t.Fatalf("PushRequestLogs(nil) error = %v, want nil", err)
	}
	if called {
		t.Fatal("PushRequestLogs(nil) reached the server, want it to short-circuit on an empty batch")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./service/aiServeWeaveGateway/controlplaneclient/... -run PushRequestLogs -v`
Expected: FAIL (compile error)

- [ ] **Step 3: Write `requestlogs.go`**

```go
// requestlogs.go is the Gateway's side of the control plane's request-log
// push API, per STATUS.md's P09/C28. It mirrors jobs.go's shape (a plain
// net/http client authenticated by the shared InternalToken) but is
// intentionally simpler: there is no read-back, no conflict translation, no
// ErrOutcomeUnknown vocabulary, because the caller (httpapi's
// requestLogPusher) never acts on a partial failure beyond logging it — a
// dropped batch of diagnostic records is not a condition anything upstream
// needs to distinguish from "the control plane momentarily answered
// slowly".
//
// requestlogs.go 是 Gateway 一侧的控制面请求日志推送 API，对应 STATUS.md 的
// P09/C28。它形态上与 jobs.go 相仿(一个由共享 InternalToken 认证的普通
// net/http 客户端)，但刻意更简单：没有读回、没有冲突转译、没有
// ErrOutcomeUnknown 这套词汇，因为调用方(httpapi 的 requestLogPusher)除了
// 记日志之外从不对一次部分失败采取任何行动——一批诊断性记录被丢弃，不是任何
// 上游需要把它与"控制面这一刻答得慢了一点"区分开的情形。
package controlplaneclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

// DefaultRequestLogsTimeout bounds one push call.
//
// DefaultRequestLogsTimeout 限制单次推送调用。
const DefaultRequestLogsTimeout = 3 * time.Second

// RequestLogsClientConfig configures a RequestLogsClient.
//
// RequestLogsClientConfig 配置一个 RequestLogsClient。
type RequestLogsClientConfig struct {
	Endpoint   string
	Token      string
	Timeout    time.Duration
	HTTPClient *http.Client
}

// RequestLogsClient is the Gateway's side of the control plane's
// request-log push API. It implements httpapi.RequestLogClient.
//
// RequestLogsClient 是 Gateway 一侧的控制面请求日志推送 API。它实现
// httpapi.RequestLogClient。
type RequestLogsClient struct {
	endpoint string
	token    string
	client   *http.Client
}

// NewRequestLogsClient returns a RequestLogsClient, validating what a typo
// would otherwise turn into a silently-empty search table.
//
// NewRequestLogsClient 返回一个 RequestLogsClient，校验那些一旦写错、就会
// 变成"检索表悄悄空着"的东西。
func NewRequestLogsClient(cfg RequestLogsClientConfig) (*RequestLogsClient, error) {
	endpoint := strings.TrimSuffix(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		return nil, errors.New("controlplaneclient: an endpoint is required")
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return nil, errors.New("controlplaneclient: the endpoint must include a scheme, e.g. http://127.0.0.1:8090")
	}
	if cfg.Token == "" {
		return nil, errors.New("controlplaneclient: a token is required; it must match the control plane's InternalToken")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultRequestLogsTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &RequestLogsClient{endpoint: endpoint, token: cfg.Token, client: client}, nil
}

// PushRequestLogs implements httpapi.RequestLogClient. An empty batch is a
// no-op that never reaches the network — the pusher's own flush already
// guards against calling with nothing to send, but a second guard here
// costs nothing and keeps this method safe to call directly.
//
// PushRequestLogs 实现 httpapi.RequestLogClient。空批次是一个从不触网的
// 空操作——推送器自己的 flush 已经防住了"无内容可发送时调用"的情形，但这里
// 再加一道防护不花什么代价，也让这个方法在被直接调用时依然安全。
func (c *RequestLogsClient) PushRequestLogs(ctx context.Context, records []httpapi.RequestLogRecord) error {
	if len(records) == 0 {
		return nil
	}
	var wire struct {
		Records []struct {
			RequestID  string    `json:"request_id"`
			TenantID   string    `json:"tenant_id"`
			KeyDisplay string    `json:"key_display,omitempty"`
			Endpoint   string    `json:"endpoint"`
			StatusCode int       `json:"status_code"`
			Outcome    string    `json:"outcome"`
			DurationMS int64     `json:"duration_ms"`
			CreatedAt  time.Time `json:"created_at"`
		} `json:"records"`
	}
	wire.Records = make([]struct {
		RequestID  string    `json:"request_id"`
		TenantID   string    `json:"tenant_id"`
		KeyDisplay string    `json:"key_display,omitempty"`
		Endpoint   string    `json:"endpoint"`
		StatusCode int       `json:"status_code"`
		Outcome    string    `json:"outcome"`
		DurationMS int64     `json:"duration_ms"`
		CreatedAt  time.Time `json:"created_at"`
	}, len(records))
	for i, r := range records {
		wire.Records[i].RequestID = r.RequestID
		wire.Records[i].TenantID = r.TenantID
		wire.Records[i].KeyDisplay = r.KeyDisplay
		wire.Records[i].Endpoint = r.Endpoint
		wire.Records[i].StatusCode = r.StatusCode
		wire.Records[i].Outcome = r.Outcome
		wire.Records[i].DurationMS = r.DurationMS
		wire.Records[i].CreatedAt = r.CreatedAt
	}

	encoded, err := json.Marshal(wire)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/internal/v1/requestlogs", bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("controlplaneclient: reaching the control plane: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("controlplaneclient: the control plane answered %d", resp.StatusCode)
	}
	return nil
}
```

(The doubly-declared anonymous struct above is deliberately explicit rather than DRY, matching this package's existing `jobWire`-style separation between the internal wire shape and the public type — if it reads awkwardly once written, refactor it into one named `requestLogRecordWire` struct used both for the slice type and the loop, which is cleaner; either is acceptable, but do not skip the JSON tag mapping.)

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./service/aiServeWeaveGateway/controlplaneclient/... -run RequestLogs -v`
Expected: PASS

- [ ] **Step 5: Wire `httpapi.Config`**

In `httpapi.go`, add fields to `Config` next to `JobPersistClient`/`PersistInterval` etc.:

```go
	// RequestLogClient pushes batches of authenticated front-door request
	// records to the control plane's internal API (STATUS.md's P09/C28).
	// Nil disables request logging entirely — a deployment with no control
	// plane gets no searchable request history, the same nil-degrades
	// pattern JobPersistClient already follows.
	RequestLogClient RequestLogClient
	// RequestLogBufferSize bounds the in-memory push buffer. Zero uses
	// DefaultRequestLogBufferSize.
	RequestLogBufferSize int
	// RequestLogBatchSize bounds how many records one push call carries.
	// Zero uses DefaultRequestLogBatchSize.
	RequestLogBatchSize int
	// RequestLogFlushInterval is the maximum time a record waits in the
	// buffer before being pushed. Zero uses DefaultRequestLogFlushInterval.
	RequestLogFlushInterval time.Duration
```

In `New()`, next to the `persister` block:

```go
	var requestLogPusher *requestLogPusher
	if cfg.RequestLogClient != nil {
		requestLogPusher = newRequestLogPusher(cfg.RequestLogClient, clock, logger, requestLogPushConfig{
			BufferSize:    cfg.RequestLogBufferSize,
			BatchSize:     cfg.RequestLogBatchSize,
			FlushInterval: cfg.RequestLogFlushInterval,
		})
		go requestLogPusher.run()
		h.requestLogs = requestLogPusher
	}
```

Add `requestLogPusher *requestLogPusher` to the `Server` struct, set it in the returned `&Server{...}` literal, and in `Close()`:

```go
	if s.requestLogPusher != nil {
		s.requestLogPusher.Stop()
	}
```

Change `Handler: h.observe(withLogging(logger, auth.middleware(h.rateLimit(mux))))` to insert the new middleware between `auth.middleware` and `h.rateLimit`:

```go
		Handler: h.observe(withLogging(logger, auth.middleware(h.requestLogMiddleware(h.rateLimit(mux))))),
```

- [ ] **Step 6: Wire `main.go`**

Add an adapter function next to `jobPersistenceAdapter`:

```go
// requestLogClientAdapter builds the Gateway's side of the control plane's
// request-log push API, or returns nil when no control plane is configured
// — mirroring jobPersistenceAdapter's own degrade path and its "return the
// concrete type" reasoning: see that function's doc comment for why a nil
// *controlplaneclient.RequestLogsClient must never be assigned directly
// into an httpapi.Config interface field.
//
// requestLogClientAdapter 构建 Gateway 一侧的控制面请求日志推送 API 客户端，
// 或在未配置控制面时返回 nil——与 jobPersistenceAdapter 自己的退化路径及其
// "返回具体类型"的理由相同：为什么一个 nil 的
// *controlplaneclient.RequestLogsClient 绝不能被直接赋给 httpapi.Config 的
// 接口字段，见该函数的文档注释。
func requestLogClientAdapter(addr, token string, logger *slog.Logger) (*controlplaneclient.RequestLogsClient, error) {
	if addr == "" {
		return nil, nil
	}
	if token == "" {
		token = os.Getenv(controlPlaneTokenEnv)
	}
	client, err := controlplaneclient.NewRequestLogsClient(controlplaneclient.RequestLogsClientConfig{
		Endpoint: addr,
		Token:    token,
	})
	if err != nil {
		return nil, err
	}
	logger.Info("pushing request logs to the control plane", slog.String("control_plane_addr", addr))
	return client, nil
}
```

Near where `jobPersistence` is built and assigned, add:

```go
	requestLogClient, err := requestLogClientAdapter(*controlPlaneAddr, *controlPlaneToken, logger)
	if err != nil {
		return err // match this function's existing error-handling style at this point — read the surrounding lines to use the exact idiom (fmt.Errorf wrap / log.Fatal / return, whichever this file already uses here)
	}
	if requestLogClient != nil {
		httpCfg.RequestLogClient = requestLogClient
	}
```

- [ ] **Step 7: Full Gateway build and test**

Run: `go build ./service/aiServeWeaveGateway/...`
Run: `go test ./service/aiServeWeaveGateway/... -race`
Expected: both succeed

- [ ] **Step 8: Commit**

```bash
git add service/aiServeWeaveGateway/controlplaneclient/requestlogs.go \
  service/aiServeWeaveGateway/controlplaneclient/requestlogs_test.go \
  service/aiServeWeaveGateway/httpapi/httpapi.go \
  service/aiServeWeaveGateway/main.go
git commit -m "$(cat <<'EOF'
feat(gateway): wire the request-log pusher into the front door and main.go

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 12: Console — contract parser and upstream allowlist

**Files:**
- Modify: `service/aiServeWeaveConsole/lib/console/contract.ts` (add `RequestLogEntry` + `parseRequestLogEntries`)
- Modify: `service/aiServeWeaveConsole/lib/console/upstream-routes.ts` (add two routes)
- Modify: `service/aiServeWeaveConsole/lib/console/contract.test.ts` (add tests)
- Modify: `service/aiServeWeaveConsole/lib/console/upstream-routes.test.ts` (add tests)

**Interfaces:**
- Consumes: `record`, `text`, `optionalText`, `timestamp`, `parsePage` (existing helpers in `contract.ts`, same file `parseAuditEntries` already uses).
- Produces: `interface RequestLogEntry`, `function parseRequestLogEntries(value: unknown): Page<RequestLogEntry>`.

- [ ] **Step 1: Write the failing contract test**

Add to `contract.test.ts`, near `test("audit entries keep the fields...")`:

```typescript
test("request log entries keep the fields the API actually returns", () => {
  const page = parseRequestLogEntries({
    items: [
      {
        request_id: "req_1",
        key_display: "aisw-abcd1234",
        endpoint: "chat",
        status_code: 200,
        outcome: "ok",
        duration_ms: 842,
        created_at: "2026-09-11T08:00:00Z",
      },
    ],
    next_cursor: "",
  });
  assert.deepEqual(page.items[0], {
    requestId: "req_1",
    tenantId: "",
    keyDisplay: "aisw-abcd1234",
    endpoint: "chat",
    statusCode: 200,
    outcome: "ok",
    durationMs: 842,
    createdAt: "2026-09-11T08:00:00Z",
  });
  assert.equal(page.nextCursor, null);
});

test("a request log entry the Console cannot trust is refused", () => {
  assert.throws(() => parseRequestLogEntries({ items: [{ request_id: "req_1" }] }), ApiError);
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `pnpm --dir service/aiServeWeaveConsole test 2>&1 | grep -A5 "request log entries"`
Expected: FAIL (compile error — `parseRequestLogEntries` is not exported yet)

- [ ] **Step 3: Add the interface and parser to `contract.ts`, near `AuditEntry`/`parseAuditEntries`**

```typescript
/**
 * RequestLogEntry mirrors types.RequestLogResponse: one persisted,
 * authenticated OpenAI front-door request (STATUS.md's P09/C28). tenantId
 * is "" on the tenant-scoped endpoint, where the API omits it because it is
 * already implied by the caller's own session.
 *
 * RequestLogEntry 镜像 types.RequestLogResponse：一条持久化的、已通过鉴权的
 * OpenAI 前门请求(STATUS.md 的 P09/C28)。在按租户限定的端点上 tenantId 为
 * ""，因为 API 在那里省略了它——它已经由调用方自己的会话隐含。
 */
export interface RequestLogEntry {
  requestId: string;
  tenantId: string;
  keyDisplay: string;
  endpoint: string;
  statusCode: number;
  outcome: string;
  durationMs: number;
  createdAt: string;
}

/**
 * parseRequestLogEntries validates one page of `GET /admin/v1/requests` or
 * `GET /operator/v1/requests`.
 *
 * parseRequestLogEntries 校验 `GET /admin/v1/requests` 或
 * `GET /operator/v1/requests` 的一页。
 */
export function parseRequestLogEntries(value: unknown): Page<RequestLogEntry> {
  return parsePage(value, (item) => {
    const source = record(item);
    return {
      requestId: text(source, "request_id"),
      tenantId: optionalText(source, "tenant_id"),
      keyDisplay: text(source, "key_display"),
      endpoint: text(source, "endpoint"),
      statusCode: count(source, "status_code"),
      outcome: text(source, "outcome"),
      durationMs: count(source, "duration_ms"),
      createdAt: timestamp(source, "created_at"),
    };
  });
}
```

`count` is `contract.ts`'s existing helper for an `omitempty` non-negative integer field (confirmed at `contract.ts:292`, used the same way for other numeric fields); `status_code` and `duration_ms` are both always non-negative, so it applies directly.

- [ ] **Step 4: Run to verify it passes**

Run: `pnpm --dir service/aiServeWeaveConsole test 2>&1 | grep -A5 "request log entries"`
Expected: PASS

- [ ] **Step 5: Add the two upstream routes**

In `upstream-routes.ts`'s `ROUTES` array, near the `jobs/history` entries:

```typescript
  {
    method: "GET",
    segments: [literal("admin"), literal("v1"), literal("requests")],
    query: ["limit", "cursor", "since", "until", "status", "request_id"],
  },
```

In `OPERATOR_ROUTES`, near the `metrics/history` entry:

```typescript
  { method: "GET", segments: [literal("operator"), literal("v1"), literal("requests")], query: ["limit", "cursor", "since", "until", "status", "request_id", "tenant_id"] },
```

- [ ] **Step 6: Write the allowlist tests**

Read `upstream-routes.test.ts`'s existing test for `/operator/v1/metrics/history` (`"operator metrics history route forwards since/until only"`) first for the exact resolver-call idiom, then add two analogous tests: one confirming `GET /admin/v1/requests?limit=50&cursor=x&since=...&until=...&status=ok&request_id=req_1` resolves and forwards exactly those query parameters (and drops an unlisted one, e.g. `tenant_id`, on the tenant surface), and one confirming `GET /operator/v1/requests?tenant_id=tnt_1` resolves on the operator surface.

- [ ] **Step 7: Run**

Run: `pnpm --dir service/aiServeWeaveConsole test`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add service/aiServeWeaveConsole/lib/console/contract.ts \
  service/aiServeWeaveConsole/lib/console/contract.test.ts \
  service/aiServeWeaveConsole/lib/console/upstream-routes.ts \
  service/aiServeWeaveConsole/lib/console/upstream-routes.test.ts
git commit -m "$(cat <<'EOF'
feat(console): parse and allowlist the request-log search endpoints

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 13: Console — shared view component, pages and navigation

**Files:**
- Create: `service/aiServeWeaveConsole/app/console/requests/request-log-view.tsx`
- Create: `service/aiServeWeaveConsole/app/console/requests/page.tsx`
- Create: `service/aiServeWeaveConsole/app/operator/(protected)/requests/page.tsx`
- Modify: `service/aiServeWeaveConsole/app/console/console-shell.tsx` (nav entry)
- Modify: `service/aiServeWeaveConsole/app/operator/operator-shell.tsx` (nav entry)

**Interfaces:**
- Consumes: `usePagedResource`, `useUrlFilters`, `Pager`, `ErrorState`, `LoadingState`, `parseRequestLogEntries`, `RequestLogEntry` (Task 12) — all following `app/console/audit/audit-view.tsx`'s exact composition.

- [ ] **Step 1: Write `request-log-view.tsx`, adapted directly from `audit-view.tsx`'s structure**

Read `app/console/audit/audit-view.tsx` in full first (already the template used throughout this task), then write a `RequestLogView` component with the same `{ surface = "admin" }` prop shape, the same `useUrlFilters`/`usePagedResource`/`Pager`/virtualized-table composition, but with:
  - Filter keys: `["since", "until", "status", "request_id", "size"]` (plus `"tenant_id"` only meaningful on the operator surface — read it unconditionally via `useUrlFilters` but only render its input and only pass it into `usePagedResource`'s `filters` object when `surface === "operator"`).
  - No debounced actor-id search (there is no free-text actor field here) — a plain `request_id` text input is enough, no debounce needed since a request id is typically pasted whole rather than typed character by character; still fine to reuse `useDebouncedValue` for consistency with `audit-view.tsx` if that reads more naturally once written.
  - A `status` filter rendered as a `Select` over the closed `Outcome` values (`ok`, `invalid_request`, `unauthorized`, `forbidden`, `not_found`, `rate_limited`, `internal`, `upstream_unavailable`, `error`) plus an "全部状态" option — mirror the `audit-action` `Select` exactly.
  - Table columns: 时间 (`createdAt`, `formatDateTime`), 状态码 (`statusCode`), 结果 (`outcome`), 接口 (`endpoint`), 耗时 (`durationMs`, rendered as `${value} ms`), Key (`keyDisplay`, `font-mono text-xs`), and — only when `surface === "operator"` — 租户 (`tenantId`). Since TanStack Table's column array is currently defined as a module-level constant in `audit-view.tsx`, and this view needs a surface-conditional column, build the `columns` array inside the component function (via `React.useMemo` keyed on `surface`) instead of hoisting it to module scope — that is a deliberate, small deviation from `audit-view.tsx`'s pattern, made necessary by the one column that must appear only on one surface.
  - Path: `path: surface === "operator" ? "/operator/v1/requests" : "/admin/v1/requests"`.
  - Heading and empty-state copy: "请求检索" / "当前租户已通过鉴权的 chat/responses/embeddings/models 请求，脱敏元数据，仅覆盖最近 30 天。" for the tenant surface; "跨租户的请求检索，用于排查与客户支持。" for the operator surface.

- [ ] **Step 2: Write the two thin page wrappers**

`app/console/requests/page.tsx`, copied from `app/console/audit/page.tsx`'s shape:

```typescript
import { redirect } from "next/navigation";

import { RequestLogView } from "@/app/console/requests/request-log-view";
import { readSession } from "@/lib/server/session";

/**
 * The request search page. Every signed-in tenant user may read their own
 * tenant's authenticated front-door requests (STATUS.md's P09/C28).
 *
 * 请求检索页。任何已登录的租户用户都可以读取自己租户已通过鉴权的前门请求
 * (STATUS.md 的 P09/C28)。
 */
export default async function RequestsPage() {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }
  return <RequestLogView />;
}
```

`app/operator/(protected)/requests/page.tsx`, copied from the operator audit page's shape:

```typescript
import { Suspense } from "react";
import { RequestLogView } from "@/app/console/requests/request-log-view";
import { LoadingState } from "@/components/console/states";

/** RequestsPage reads only the platform cross-tenant request search surface.
 * RequestsPage 仅读取平台跨租户请求检索入口。 */
export default function RequestsPage() {
  return <Suspense fallback={<LoadingState label="正在读取请求记录" />}><RequestLogView surface="operator" /></Suspense>;
}
```

- [ ] **Step 3: Add navigation entries**

In `console-shell.tsx`'s `NAV_ITEMS`, add `{ href: "/console/requests", label: "请求检索" }` (placed after `{ href: "/console/audit", label: "审计" }`, before `quota`, matching the existing ordering of read-oriented pages).

In `operator-shell.tsx`, add `{ href: "/operator/requests", label: "请求检索" }` (placed after `{ href: "/operator/audit", label: "运维审计" }`, before `metrics`).

- [ ] **Step 4: Build and typecheck**

Run: `pnpm --dir service/aiServeWeaveConsole build`
Run: `pnpm --dir service/aiServeWeaveConsole typecheck`
Run: `pnpm --dir service/aiServeWeaveConsole lint`
Expected: all succeed (remember `typecheck` needs `.next/types` from the `build` that just ran, per this Console's AGENTS.md)

- [ ] **Step 5: Manual browser verification**

Start the Console dev server against a running control plane with at least one `request_logs` row seeded (or accept an empty-state screenshot if no backend is available in this environment), visit `/console/requests` and `/operator/requests`, and confirm: the list renders, the status/date/request-id filters narrow it, pagination advances, and the operator view shows the tenant column while the tenant view does not. Record whichever of these could actually be exercised in this environment — do not claim an interactive check that did not happen.

- [ ] **Step 6: Commit**

```bash
git add service/aiServeWeaveConsole/app/console/requests \
  "service/aiServeWeaveConsole/app/operator/(protected)/requests" \
  service/aiServeWeaveConsole/app/console/console-shell.tsx \
  service/aiServeWeaveConsole/app/operator/operator-shell.tsx
git commit -m "$(cat <<'EOF'
feat(console): add tenant and operator request-search pages (C28)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 14: Full quality gates and documentation

**Files:**
- Modify: `STATUS.md` (check P09's C28 sub-scope, update the P09 row's description to note C28 is done and C29 remains)
- Modify: `service/aiServeWeaveGateway/README.md` (document the request-log middleware/pusher, per AGENTS.md's "改公共 API 时同步更新文档")
- Modify: `service/aiServeWeaveControlPlane/README.md` (document the new internal push endpoint, the two search endpoints, and the retention sweeper)
- Modify: `service/aiServeWeaveConsole/STATUS.md` (mark C28 done, point at its implementation)
- Modify: `CHANGELOG.md`

**Interfaces:** none — documentation and verification only.

- [ ] **Step 1: Run the full Go quality gate from the root AGENTS.md**

```bash
gofmt -l ./service ./api
go vet ./...
go build ./...
go generate ./api/...
git diff --exit-code -- api/  # confirms generated code matches the .proto — this task touches no .proto, so this should already be clean
go test ./...
go test -race ./service/...
```

Expected: `gofmt -l` prints nothing; everything else passes. If any real PostgreSQL/MySQL instance is reachable in this environment via the repo's existing `AISW_..._TEST_DSN` convention, also run the live-gated tests from Tasks 1–7 against it; otherwise note explicitly in the final report that only the default (non-live) suite was verified.

- [ ] **Step 2: Run the full Console quality gate**

```bash
pnpm --dir service/aiServeWeaveConsole lint
pnpm --dir service/aiServeWeaveConsole build
pnpm --dir service/aiServeWeaveConsole typecheck
pnpm --dir service/aiServeWeaveConsole test
```

Expected: all pass.

- [ ] **Step 3: Update `STATUS.md`**

Change the P09 row (currently `| [ ] | P09 | 请求检索与告警 | ... |`) to reflect that C28 is done and C29 is not — read the row's current exact wording first and edit it narrowly (do not rewrite the whole row), following the same style other rows use when only part of a multi-part task is complete (e.g. how P01's row text distinguishes "已实现并测试" pieces from remaining ones). Link to the new Gateway/ControlPlane README sections and to `docs/superpowers/specs/2026-09-11-p09-request-search-design.md`.

- [ ] **Step 4: Update the service READMEs and Console STATUS**

- Gateway README: add a short subsection (matching the existing numbered-list style, e.g. after the workflow-Job section) describing the request-log middleware's position in the chain, the outcome mapping table, and the bounded-push/drop behavior with its metric name.
- ControlPlane README: add a subsection under wherever the Job persistence contract section lives, describing `request_logs`'s schema, the `/internal/v1/requestlogs` push endpoint's idempotency, the two search endpoints, and the retention default — mirroring how the metrics-history section is written.
- Console STATUS.md: change C28's row from `[ ]` to `[x]`, with a one-line pointer to `app/console/requests/request-log-view.tsx` and the two page files, matching how C27's row was written when P08 completed it.

- [ ] **Step 5: Update `CHANGELOG.md`**

Add an entry under the unreleased/next section (read the file's current top entries first to match its exact heading and bullet style) describing: new `request_logs` table and retention; new Gateway request-log middleware and background pusher; new `/internal/v1/requestlogs`, `/admin/v1/requests`, `/operator/v1/requests` endpoints; new Console `/console/requests` and `/operator/requests` pages.

- [ ] **Step 6: Final commit**

```bash
git add STATUS.md service/aiServeWeaveGateway/README.md service/aiServeWeaveControlPlane/README.md \
  service/aiServeWeaveConsole/STATUS.md CHANGELOG.md
git commit -m "$(cat <<'EOF'
docs: record P09a request search (C28) as complete

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```
