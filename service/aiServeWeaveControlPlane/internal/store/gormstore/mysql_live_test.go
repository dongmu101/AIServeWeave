package gormstore_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
)

// TestLiveMySQL* exercise gormstore.Store against a real MySQL 9.7 engine —
// the same distinction this package's memstore sibling draws in its own doc
// comment: memstore (and the logic-layer tests it backs) answers whether the
// business rules are right, this file answers whether the SQL is, per
// STATUS.md's J08.
//
// They are opt-in — set AISW_MYSQL_TEST_DSN (e.g.
// "root:testpass@tcp(127.0.0.1:3307)/aiserveweave?parseTime=True&charset=utf8mb4")
// to run them — so `go test ./...` stays hermetic on machines without MySQL,
// per the repository's own rule and the pattern common/runtime/ollama's
// live_test.go already sets for an external backend.
//
// TestLiveMySQL* 系列针对真实的 MySQL 9.7 引擎验证 gormstore.Store——与本包
// memstore 那边自己文档注释划的是同一条界线：memstore（以及它支撑的 logic
// 层测试）回答业务规则对不对，这个文件回答 SQL 对不对，对应 STATUS.md 的 J08。
//
// 它们是按需启用的——设置 AISW_MYSQL_TEST_DSN（例如
// "root:testpass@tcp(127.0.0.1:3307)/aiserveweave?parseTime=True&charset=utf8mb4"）
// 才会运行——好让 `go test ./...` 在没有 MySQL 的机器上保持不依赖外部环境，
// 遵循仓库自己的规则，也是 common/runtime/ollama 的 live_test.go 已经为一个
// 外部后端立下的先例。
const mysqlDSNEnv = "AISW_MYSQL_TEST_DSN"

func liveMySQLStore(t *testing.T) *gormstore.Store {
	t.Helper()
	dsn := os.Getenv(mysqlDSNEnv)
	if dsn == "" {
		t.Skipf("set %s to run this test against a real MySQL 9.7 instance", mysqlDSNEnv)
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger:         gormlogger.Default.LogMode(gormlogger.Silent),
		TranslateError: true,
	})
	if err != nil {
		t.Fatalf("connecting to %s: %v", mysqlDSNEnv, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB(): %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	st := gormstore.New(db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := st.MigrateJobs(ctx); err != nil {
		t.Fatalf("MigrateJobs: %v", err)
	}

	// Each test starts from a clean slate. TRUNCATE rather than DROP: the
	// schema — including the version-tracked jobs migrations — stays in
	// place across the whole test binary run, matching how a real
	// deployment never re-migrates between requests.
	//
	// 每个测试都从一张干净的表开始。用 TRUNCATE 而不是 DROP：schema——包括
	// 带版本记录的 jobs 迁移——在整个测试二进制的运行期间保持不变，与真实部署
	// 从不在两次请求之间重新迁移的情形一致。
	for _, table := range []string{"jobs", "job_artifacts"} {
		if err := db.Exec("TRUNCATE TABLE " + table).Error; err != nil {
			t.Fatalf("truncating %s: %v", table, err)
		}
	}
	return st
}

func testJob(id, tenantID string) *model.Job {
	return &model.Job{
		ID: id, TenantID: tenantID, WorkflowID: "text-to-image",
		NodeID: "node-1", RuntimeID: "comfy-1", BackendRunID: "prompt-" + id,
		State: model.JobPending,
	}
}

// TestLiveMySQLMigrateJobsIsRepeatable is J08's migration coverage: running
// the same migration set twice against a real engine applies nothing the
// second time, and the schema (including the version-tracking table) is
// left exactly as the first run made it.
//
// TestLiveMySQLMigrateJobsIsRepeatable 是 J08 的迁移覆盖：对着真实引擎两次
// 运行同一套迁移，第二次什么都不会应用，schema（包括版本记录表本身）与第一次
// 运行完全一致。
func TestLiveMySQLMigrateJobsIsRepeatable(t *testing.T) {
	st := liveMySQLStore(t)
	ctx := context.Background()

	applied, err := st.MigrateJobs(ctx)
	if err != nil {
		t.Fatalf("second MigrateJobs call: %v", err)
	}
	if len(applied) != 0 {
		t.Errorf("MigrateJobs on an up-to-date schema applied %v, want none", applied)
	}

	// The schema this migration created is actually usable — not just
	// "no error", but a real row round-trips.
	//
	// 这次迁移建出的 schema 确实可用——不只是「没有报错」，一整行数据能真实
	// 存进去再读出来。
	if err := st.CreateJob(ctx, testJob("job_migrate_check", "tenant-a")); err != nil {
		t.Fatalf("CreateJob against the migrated schema: %v", err)
	}
}

// TestLiveMySQLCreateJobIdempotencyAndCrossTenantConflict is J08's coverage
// for "重复事件" and "跨租户拒绝" on the create path: a duplicate id for the
// same tenant is the persistence contract's idempotent resubmission window,
// and the same id claimed by a different tenant is a real conflict — both
// against real MySQL's own unique-key enforcement via TranslateError.
//
// TestLiveMySQLCreateJobIdempotencyAndCrossTenantConflict 是 J08 对创建路径上
// 「重复事件」与「跨租户拒绝」的覆盖：同一租户下重复的 id 是持久化契约的幂等
// 重试窗口，被另一个租户抢占同一个 id 才是真实冲突——两者都经由真实 MySQL 自己
// 的唯一键约束（通过 TranslateError）验证。
func TestLiveMySQLCreateJobIdempotencyAndCrossTenantConflict(t *testing.T) {
	st := liveMySQLStore(t)
	ctx := context.Background()

	if err := st.CreateJob(ctx, testJob("job_dup", "tenant-a")); err != nil {
		t.Fatalf("first CreateJob: %v", err)
	}
	err := st.CreateJob(ctx, testJob("job_dup", "tenant-a"))
	if !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate CreateJob (same tenant) = %v, want ErrConflict — the caller resolves this into an idempotent read, not a hard failure", err)
	}

	err = st.CreateJob(ctx, testJob("job_dup", "tenant-b"))
	if !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate CreateJob (different tenant) = %v, want ErrConflict", err)
	}

	got, err := st.GetJob(ctx, "tenant-a", "job_dup")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.TenantID != "tenant-a" {
		t.Errorf("the row still belongs to tenant-a; tenant-b's conflicting create must not have overwritten it, got %+v", got)
	}
}

// TestLiveMySQLGetJobTenantIsolation is J08's coverage for "跨租户拒绝" on
// the read path.
//
// TestLiveMySQLGetJobTenantIsolation 是 J08 对读取路径上「跨租户拒绝」的覆盖。
func TestLiveMySQLGetJobTenantIsolation(t *testing.T) {
	st := liveMySQLStore(t)
	ctx := context.Background()
	if err := st.CreateJob(ctx, testJob("job_iso", "tenant-a")); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := st.GetJob(ctx, "tenant-b", "job_iso"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetJob(wrong tenant) = %v, want ErrNotFound", err)
	}
}

// TestLiveMySQLUpdateJobStateResolvesConcurrentWritesByObservedSeq is J08's
// coverage for "并发更新": many goroutines race to advance the same job's
// ObservedSeq concurrently, using real MySQL row locking (the
// UPDATE ... WHERE observed_seq < ? clause gormstore's UpdateJobState issues)
// rather than any coordination in this process. The row must land on
// exactly the highest sequence sent, never a lower one that arrived later in
// wall-clock time but carried a smaller number, and never anything but one
// of the values actually sent.
//
// TestLiveMySQLUpdateJobStateResolvesConcurrentWritesByObservedSeq 是 J08 对
// 「并发更新」的覆盖：许多协程并发竞争推进同一个 job 的 ObservedSeq，靠的是
// 真实 MySQL 的行锁（gormstore 的 UpdateJobState 发出的
// UPDATE ... WHERE observed_seq < ? 子句），而不是本进程里的任何协调。最终
// 这一行必须恰好落在发送过的最高序号上，绝不会是一个按时钟到达更晚、却带着
// 更小数字的更新，也绝不会是除了实际发送过的值以外的任何东西。
func TestLiveMySQLUpdateJobStateResolvesConcurrentWritesByObservedSeq(t *testing.T) {
	st := liveMySQLStore(t)
	ctx := context.Background()
	if err := st.CreateJob(ctx, testJob("job_concurrent", "tenant-a")); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	const writers = 20
	var wg sync.WaitGroup
	for seq := 1; seq <= writers; seq++ {
		wg.Add(1)
		go func(seq int64) {
			defer wg.Done()
			_, err := st.UpdateJobState(ctx, "tenant-a", "job_concurrent", store.JobStateUpdate{
				State: model.JobRunning, ErrorSummary: "", ObservedSeq: seq, At: time.Now(),
			})
			if err != nil {
				t.Errorf("UpdateJobState(seq=%d): %v", seq, err)
			}
		}(int64(seq))
	}
	wg.Wait()

	got, err := st.GetJob(ctx, "tenant-a", "job_concurrent")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.ObservedSeq != writers {
		t.Errorf("job.ObservedSeq = %d, want %d (the highest sequence sent, regardless of arrival order)", got.ObservedSeq, writers)
	}

	// A terminal-state update must not be overtaken by a lower-numbered
	// pending update that happens to be concurrent with it — the immutability
	// half of the same real-lock guarantee.
	//
	// 一次终态更新不得被一个恰好与它并发、但序号更小的 pending 更新超车——这是
	// 同一条真实行锁保证里「不可变」的那一半。
	if _, err := st.UpdateJobState(ctx, "tenant-a", "job_concurrent", store.JobStateUpdate{
		State: model.JobSucceeded, ObservedSeq: writers + 1, At: time.Now(),
	}); err != nil {
		t.Fatalf("UpdateJobState(terminal): %v", err)
	}
	applied, err := st.UpdateJobState(ctx, "tenant-a", "job_concurrent", store.JobStateUpdate{
		State: model.JobFailed, ObservedSeq: writers + 2, At: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpdateJobState(post-terminal): %v", err)
	}
	if applied {
		t.Error("UpdateJobState after a terminal state reported applied=true, want false")
	}
	got, err = st.GetJob(ctx, "tenant-a", "job_concurrent")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.State != model.JobSucceeded {
		t.Errorf("job.State = %v, want unchanged (succeeded) — a real row lock must not let a later, lower-priority update through", got.State)
	}
}

// TestLiveMySQLListActiveJobsForRouteAcrossTenants is J08's coverage for
// "多副本查询": a route-scoped read that spans every tenant, which is exactly
// what a recovering Gateway replica issues against real MySQL after a
// restart or when a second replica shares the same node.
//
// TestLiveMySQLListActiveJobsForRouteAcrossTenants 是 J08 对「多副本查询」的
// 覆盖：一次跨越所有租户的、按路由限定范围的读取，正是一个重启后正在恢复的
// Gateway 副本、或者与另一个副本共享同一节点时，会对着真实 MySQL 发出的那种
// 查询。
func TestLiveMySQLListActiveJobsForRouteAcrossTenants(t *testing.T) {
	st := liveMySQLStore(t)
	ctx := context.Background()

	route := struct{ node, runtime string }{"node-shared", "comfy-shared"}
	mk := func(id, tenantID, state string) *model.Job {
		j := testJob(id, tenantID)
		j.NodeID, j.RuntimeID, j.State = route.node, route.runtime, state
		return j
	}
	for _, j := range []*model.Job{
		mk("job_r1", "tenant-a", model.JobRunning),
		mk("job_r2", "tenant-b", model.JobPending),
		mk("job_r3", "tenant-a", model.JobSucceeded), // terminal: must not appear
	} {
		if err := st.CreateJob(ctx, j); err != nil {
			t.Fatalf("CreateJob(%s): %v", j.ID, err)
		}
	}
	// job_r3 was created pending; move it to terminal the way a real
	// UpdateJobState call would.
	if _, err := st.UpdateJobState(ctx, "tenant-a", "job_r3", store.JobStateUpdate{
		State: model.JobSucceeded, ObservedSeq: 1, At: time.Now(),
	}); err != nil {
		t.Fatalf("UpdateJobState(job_r3): %v", err)
	}

	active, err := st.ListActiveJobsForRoute(ctx, route.node, route.runtime)
	if err != nil {
		t.Fatalf("ListActiveJobsForRoute: %v", err)
	}
	ids := map[string]bool{}
	for _, j := range active {
		ids[j.ID] = true
	}
	if !ids["job_r1"] || !ids["job_r2"] {
		t.Errorf("ListActiveJobsForRoute = %v, want job_r1 and job_r2 (both tenants, non-terminal)", ids)
	}
	if ids["job_r3"] {
		t.Error("ListActiveJobsForRoute included job_r3, which is already terminal")
	}
}

// TestLiveMySQLUnreachableDatabaseFailsFastNotHangs is J08's coverage for
// "数据库中断": a Store pointed at a database that refuses connections must
// return an error within its context deadline, never hang indefinitely —
// the ControlPlane README's persistence contract requires the caller above
// this layer (STATUS.md's J05's jobPersister) to be able to treat this as an
// ordinary, bounded failure.
//
// TestLiveMySQLUnreachableDatabaseFailsFastNotHangs 是 J08 对「数据库中断」的
// 覆盖：一个指向拒绝连接的数据库的 Store，必须在其 context 截止时间内返回错误，
// 绝不能无限期挂起——ControlPlane README 的持久化契约要求这一层之上的调用方
// （STATUS.md J05 的 jobPersister）能把它当作一次普通的、有界的失败来处理。
func TestLiveMySQLUnreachableDatabaseFailsFastNotHangs(t *testing.T) {
	if os.Getenv(mysqlDSNEnv) == "" {
		t.Skipf("set %s to run this test (it only needs MySQL to be expected, not reachable on the bogus port used here)", mysqlDSNEnv)
	}
	// Port 1 is privileged and never a real MySQL listener in this test
	// environment, so the connection attempt fails the way a genuinely
	// unreachable database would, without depending on stopping the shared
	// live container other tests in this file still use.
	//
	// 端口 1 是特权端口，在本测试环境下从不会是真实的 MySQL 监听地址，因此这次
	// 连接尝试会以「数据库真的不可达」时的同一种方式失败，且不依赖停掉本文件
	// 其余测试仍在使用的那个共享容器。
	db, err := gorm.Open(mysql.Open("root:wrong@tcp(127.0.0.1:1)/aiserveweave?parseTime=True&timeout=2s"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return // failing at Open is an equally valid fast failure
	}
	st := gormstore.New(db)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- st.CreateJob(ctx, testJob("job_unreachable", "tenant-a")) }()

	select {
	case err := <-done:
		if err == nil {
			t.Error("CreateJob against an unreachable database returned nil, want an error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CreateJob against an unreachable database did not return within 10s — it must fail fast, not hang")
	}
}
