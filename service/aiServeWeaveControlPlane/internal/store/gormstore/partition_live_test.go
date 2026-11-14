package gormstore_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
)

// The TestLivePartition* tests run the time-partitioning change against real
// MySQL and PostgreSQL engines: the upgrade conversion, resume and rollback,
// write idempotency on the composite keys, maintenance and retention.
//
// TestLivePartition* 系列在真实的 MySQL 与 PostgreSQL 引擎上验证按时间分区的改动：升级
// 转换、resume 与回滚、复合主键上的写入幂等、分区维护与保留期清理。

func dayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// partitionFixture is history loaded into a pre-partitioning database.
//
// partitionFixture 是写入分区之前数据库的历史数据。
type partitionFixture struct {
	now, day time.Time // day is three days ago, the partition the retention cutoff lands in
}

func newPartitionFixture() partitionFixture {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return partitionFixture{now: now, day: dayStart(now).AddDate(0, 0, -3)}
}

func (f partitionFixture) requestLogs() []model.RequestLog {
	row := func(id string, at time.Time) model.RequestLog {
		return model.RequestLog{ID: id, TenantID: "tnt_p", KeyDisplay: "aisw-p", Endpoint: "chat", StatusCode: 500, Outcome: "error",
			DurationMS: 7, CreatedAt: at, Model: "m", ErrorCode: "upstream", ErrorMessage: "boom", ErrorDetail: "detail " + id}
	}
	return []model.RequestLog{
		row("req_ancient", f.day.AddDate(0, 0, -40).Add(5*time.Hour)),
		row("req_d1", f.day.AddDate(0, 0, -1).Add(10*time.Hour)),
		row("req_d_am", f.day.Add(11*time.Hour)),
		row("req_d_pm", f.day.Add(13*time.Hour)),
		row("req_today", f.now.Add(-time.Minute)),
	}
}

func (f partitionFixture) load(t *testing.T, ctx context.Context, db *gorm.DB, st *gormstore.Store) {
	t.Helper()
	if err := st.CreateRequestLogs(ctx, f.requestLogs()); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUsageRecords(ctx, []model.UsageRecord{
		{ID: "use_old", TenantID: "tnt_p", Model: "m", Endpoint: "chat", PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3, CreatedAt: f.now.AddDate(0, 0, -70)},
		{ID: "use_recent", TenantID: "tnt_p", Model: "m", Endpoint: "chat", PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30, CreatedAt: f.now.Add(-time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertRollup(ctx, []model.MetricsHistoryPoint{
		{Metric: "m", Labels: "a=1", BucketAt: f.day.AddDate(0, 0, -1), Value: 1},
		{Metric: "m", Labels: "a=1", BucketAt: f.day, Value: 2},
		{Metric: "m", Labels: "a=1", BucketAt: dayStart(f.now), Value: 3},
	}); err != nil {
		t.Fatal(err)
	}
	for _, turn := range []model.ResponseTurn{
		{ID: "resp_old", TenantID: "tnt_p", Model: "m", Messages: `[{"role":"user"}]`, CreatedAt: f.day.Add(9 * time.Hour)},
		{ID: "resp_new", TenantID: "tnt_p", PreviousResponseID: "resp_old", Model: "m", Messages: `[{"role":"assistant"}]`, CreatedAt: f.now.Add(-time.Minute)},
	} {
		turn := turn
		if err := st.CreateResponseTurn(ctx, &turn); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range []model.AuditLog{
		{ID: "aud_old", TenantID: "tnt_p", ActorID: "usr_p", Action: "x.old", CreatedAt: f.now.AddDate(0, 0, -90)},
		{ID: "aud_recent", TenantID: "tnt_p", ActorID: "usr_p", Action: "x.recent", CreatedAt: f.now.Add(-time.Minute)},
	} {
		entry := entry
		if err := st.AppendAudit(ctx, &entry); err != nil {
			t.Fatal(err)
		}
	}
	// Released schemas allowed a NULL audit time; the upgrade must keep the row.
	// 旧表结构允许审计时间为 NULL；升级必须保留这一行。
	if err := db.Exec("INSERT INTO audit_logs (id, tenant_id, action) VALUES ('aud_null', 'tnt_p', 'x.null')").Error; err != nil {
		t.Fatal(err)
	}
}

func snapshot(t *testing.T, db *gorm.DB, table string) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := db.Table(table).Order("id").Find(&rows).Error; err != nil {
		t.Fatalf("snapshot %s: %v", table, err)
	}
	return rows
}

var partitionedTableColumns = map[string]string{
	"request_logs": "created_at", "usage_records": "created_at", "metrics_history_points": "bucket_at",
	"response_turns": "created_at", "audit_logs": "created_at",
}

// upgradedDatabase loads the fixture into a pre-partitioning schema and runs
// the real upgrade.
//
// upgradedDatabase 把夹具写入分区之前的表结构，然后执行真实的升级。
func upgradedDatabase(t *testing.T, ctx context.Context, db *gorm.DB) (*gormstore.Store, partitionFixture, map[string][]map[string]any) {
	t.Helper()
	st := gormstore.New(db)
	if err := gormstore.MigrateBeforePartitionsForTest(ctx, st); err != nil {
		t.Fatalf("pre-partition migrations: %v", err)
	}
	f := newPartitionFixture()
	f.load(t, ctx, db, st)
	before := map[string][]map[string]any{}
	for table := range partitionedTableColumns {
		before[table] = snapshot(t, db, table)
		if _, names, _, err := gormstore.PartitionLayoutForTest(db, table); err != nil || len(names) != 0 {
			t.Fatalf("%s is partitioned before the upgrade: %v, %v", table, names, err)
		}
	}
	if err := st.MigrateAll(ctx, false); err != nil {
		t.Fatalf("upgrade MigrateAll: %v", err)
	}
	if err := st.CheckSchema(ctx); err != nil {
		t.Fatalf("CheckSchema after upgrade: %v", err)
	}
	return st, f, before
}

func TestLivePartitionUpgradePreservesData(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		var maxID int64
		st, f, before := upgradedDatabase(t, ctx, db)
		for table, column := range partitionedTableColumns {
			after := snapshot(t, db, table)
			want := before[table]
			if table == "audit_logs" {
				for i, row := range after {
					if row["id"] == "aud_null" {
						if at, ok := row["created_at"].(time.Time); !ok || at.Before(f.now.Add(-time.Minute)) {
							t.Errorf("aud_null created_at = %v, want the migration time", row["created_at"])
						}
						after[i]["created_at"] = nil
					}
				}
			}
			if !reflect.DeepEqual(after, want) {
				t.Errorf("%s rows changed across the upgrade:\nbefore %v\nafter  %v", table, want, after)
			}
			gotColumn, names, catchAll, err := gormstore.PartitionLayoutForTest(db, table)
			if err != nil || gotColumn != column || catchAll == "" || len(names) < 2 {
				t.Errorf("%s layout = %q %v catch-all %q, %v", table, gotColumn, names, catchAll, err)
			}
		}
		_, names, _, _ := gormstore.PartitionLayoutForTest(db, "request_logs")
		if !hasPartitionFor(names, f.day.AddDate(0, 0, -40)) || !hasPartitionFor(names, dayStart(f.now).AddDate(0, 0, 7)) {
			t.Errorf("request_logs partitions %v should run from the oldest row's day to today + 7", names)
		}
		for table, indexes := range map[string][]string{
			"request_logs":           {"idx_request_logs_tenant_created", "idx_request_logs_tenant_outcome_created", "idx_request_logs_created"},
			"usage_records":          {"idx_usage_records_tenant_model_created", "idx_usage_records_created"},
			"metrics_history_points": {"idx_metrics_history_points_series", "idx_metrics_history_points_bucket"},
			"response_turns":         {"idx_response_turns_tenant"},
			"audit_logs":             {"idx_audit_logs_tenant_id", "idx_audit_logs_actor_id", "idx_audit_logs_action", "idx_audit_logs_created_at", "idx_audit_logs_tenant_created"},
		} {
			for _, index := range indexes {
				if !db.Migrator().HasIndex(table, index) {
					t.Errorf("index %s.%s is missing after the upgrade", table, index)
				}
			}
		}
		// The id counter must not move backwards across the conversion.
		// id 计数器不得在转换后回退。
		if err := db.Table("metrics_history_points").Select("MAX(id)").Row().Scan(&maxID); err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertRollup(ctx, []model.MetricsHistoryPoint{{Metric: "m", Labels: "a=2", BucketAt: dayStart(f.now), Value: 9}}); err != nil {
			t.Fatal(err)
		}
		var newID int64
		if err := db.Table("metrics_history_points").Where("labels = ?", "a=2").Select("id").Row().Scan(&newID); err != nil || newID <= maxID {
			t.Errorf("new metrics id = %d (err %v), want above the pre-upgrade max %d", newID, err, maxID)
		}
	})
}

func hasPartitionFor(names []string, day time.Time) bool {
	suffix := "p" + day.Format("20060102")
	for _, name := range names {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func TestLivePartitionQueriesMatchAfterUpgrade(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st, f, _ := upgradedDatabase(t, ctx, db)
		turn, err := st.GetResponseTurn(ctx, "tnt_p", "resp_new")
		if err != nil || turn.PreviousResponseID != "resp_old" {
			t.Errorf("GetResponseTurn = %+v, %v", turn, err)
		}
		if _, err := st.GetResponseTurn(ctx, "tnt_other", "resp_new"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("cross-tenant GetResponseTurn = %v, want ErrNotFound", err)
		}
		page, err := st.ListRequestLogs(ctx, store.ListQuery{}, store.RequestLogFilter{RequestID: "req_d_am"})
		if err != nil || len(page.Items) != 1 || page.Items[0].ErrorDetail != "detail req_d_am" {
			t.Errorf("ListRequestLogs(request id) = %+v, %v", page.Items, err)
		}
		summary, err := st.SummarizeUsage(ctx, store.UsageRecordFilter{TenantID: "tnt_p"})
		if err != nil || len(summary) != 1 || summary[0].TotalTokens != 33 || summary[0].RequestCount != 2 {
			t.Errorf("SummarizeUsage = %+v, %v", summary, err)
		}
		// Audit pagination walks newest first across month partitions.
		// 审计分页跨月份分区按最新优先遍历。
		for i := 0; i < 4; i++ {
			entry := model.AuditLog{ID: fmt.Sprintf("aud_page_%d", i), TenantID: "tnt_page", Action: "x", CreatedAt: f.now.AddDate(0, -i, 0)}
			if err := st.AppendAudit(ctx, &entry); err != nil {
				t.Fatal(err)
			}
		}
		var got []string
		query := store.ListQuery{Limit: 3}
		for {
			page, err := st.ListAudit(ctx, "tnt_page", query, store.AuditFilter{})
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range page.Items {
				got = append(got, e.ID)
			}
			if page.NextCursor == "" {
				break
			}
			query.Cursor = page.NextCursor
		}
		if want := []string{"aud_page_0", "aud_page_1", "aud_page_2", "aud_page_3"}; !reflect.DeepEqual(got, want) {
			t.Errorf("audit pages = %v, want %v", got, want)
		}
	})
}

func TestLivePartitionResumeAndRollback(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		if err := gormstore.MigrateBeforePartitionsForTest(ctx, st); err != nil {
			t.Fatal(err)
		}
		f := newPartitionFixture()
		f.load(t, ctx, db, st)
		if db.Name() == "postgres" {
			// A failing conversion rolls back completely on PostgreSQL.
			// PostgreSQL 上失败的转换会整体回滚。
			if err := db.Exec("CREATE TABLE request_logs_unpartitioned (id INT)").Error; err != nil {
				t.Fatal(err)
			}
			if err := st.MigrateAll(ctx, false); err == nil {
				t.Fatal("MigrateAll succeeded despite the colliding table")
			}
			if column, _, _, err := gormstore.PartitionLayoutForTest(db, "request_logs"); err != nil || column != "" {
				t.Fatalf("request_logs partitioned on %q after a failed conversion (%v)", column, err)
			}
			var recorded int64
			db.Table("schema_migrations_request_logs").Where("id = ?", "0004_partition_request_logs.sql").Count(&recorded)
			if recorded != 0 || len(snapshot(t, db, "request_logs")) != len(f.requestLogs()) {
				t.Fatalf("failed conversion left ledger rows %d or lost data", recorded)
			}
			if err := db.Exec("DROP TABLE request_logs_unpartitioned").Error; err != nil {
				t.Fatal(err)
			}
		}
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatalf("MigrateAll: %v", err)
		}
		if db.Name() == "mysql" {
			// A conversion that finished its ALTER but not its ledger update is
			// completed by an explicit resume without converting twice.
			// ALTER 已完成、账本未更新的转换，由显式 resume 补完，不会再次转换。
			_, wantNames, _, _ := gormstore.PartitionLayoutForTest(db, "request_logs")
			if err := db.Table("schema_migrations_request_logs").Where("id = ?", "0004_partition_request_logs.sql").Update("dirty", true).Error; err != nil {
				t.Fatal(err)
			}
			if err := st.MigrateAll(ctx, false); err == nil {
				t.Fatal("ordinary MigrateAll resumed a dirty migration")
			}
			if err := st.MigrateAll(ctx, true); err != nil {
				t.Fatalf("explicit resume: %v", err)
			}
			if _, names, _, _ := gormstore.PartitionLayoutForTest(db, "request_logs"); !reflect.DeepEqual(names, wantNames) {
				t.Errorf("resume changed partitions: %v -> %v", wantNames, names)
			}
		}
		if err := st.CheckSchema(ctx); err != nil {
			t.Fatalf("CheckSchema: %v", err)
		}
		if got := len(snapshot(t, db, "request_logs")); got != len(f.requestLogs()) {
			t.Errorf("request_logs has %d rows, want %d", got, len(f.requestLogs()))
		}
	})
}

func TestLivePartitionMigrationTimeout(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		if err := gormstore.MigrateBeforePartitionsForTest(ctx, gormstore.New(db)); err != nil {
			t.Fatal(err)
		}
		pool, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		// A reader holding request_logs open stalls the conversion past the timeout.
		// 一个一直打开着 request_logs 的读事务使转换超过超时时间。
		conn, err := pool.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		blocker, err := conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := blocker.QueryRowContext(ctx, "SELECT COUNT(*) FROM request_logs").Scan(&n); err != nil {
			t.Fatal(err)
		}
		st := gormstore.New(db, gormstore.WithMigrationTimeout(3*time.Second))
		err = st.MigrateAll(ctx, false)
		_ = blocker.Rollback()
		_ = conn.Close()
		if err == nil || !strings.Contains(err.Error(), "migration timeout") {
			t.Fatalf("MigrateAll = %v, want a migration timeout error", err)
		}
		if err := st.CheckSchema(ctx); err == nil {
			t.Fatal("CheckSchema passed after a timed-out migration; the service must refuse to start")
		}
		if db.Name() == "mysql" {
			var dirty int64
			db.Table("schema_migrations_request_logs").Where("dirty = ?", true).Count(&dirty)
			if dirty != 1 {
				t.Fatalf("dirty request_logs migrations = %d, want 1", dirty)
			}
		}
		// MySQL keeps the dirty mark for an explicit resume; the abandoned ALTER
		// may still hold the migration lock briefly, so retry.
		// MySQL 保留 dirty 标记等待显式 resume；被放弃的 ALTER 可能仍短暂持有迁移锁，因此重试。
		deadline := time.Now().Add(60 * time.Second)
		for {
			err = gormstore.New(db).MigrateAll(ctx, db.Name() == "mysql")
			if err == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("recovery MigrateAll: %v", err)
		}
		if err := st.CheckSchema(ctx); err != nil {
			t.Fatalf("CheckSchema after recovery: %v", err)
		}
	})
}

func TestLivePartitionWritesStayIdempotent(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		at := time.Now().UTC().Truncate(time.Millisecond)
		record := model.UsageRecord{ID: "req_dup", TenantID: "tnt_i", Model: "m", Endpoint: "chat", PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2, CreatedAt: at}
		for i := 0; i < 2; i++ {
			if err := st.CreateUsageRecords(ctx, []model.UsageRecord{record}); err != nil {
				t.Fatalf("push %d: %v", i, err)
			}
		}
		summary, err := st.SummarizeUsage(ctx, store.UsageRecordFilter{TenantID: "tnt_i"})
		if err != nil || len(summary) != 1 || summary[0].RequestCount != 1 {
			t.Fatalf("SummarizeUsage after a duplicate push = %+v, %v", summary, err)
		}

		// A retried charged push neither duplicates the record nor charges twice.
		// 重试的扣费推送既不重复记录也不重复扣费。
		if err := db.Create(&model.TenantBilling{TenantID: "tnt_c", Mode: model.BillingModePrepaid, BalanceMicro: 1000, DodoCustomerID: ""}).Error; err != nil {
			t.Fatal(err)
		}
		charged := model.UsageRecord{ID: "req_charged", TenantID: "tnt_c", Model: "m", Endpoint: "chat", TotalTokens: 5, CreatedAt: at}
		charge := model.UsageCharge{RequestID: "req_charged", TenantID: "tnt_c", Model: "m", CostMicro: 100, CreatedAt: at}
		for i := 0; i < 2; i++ {
			if _, err := st.ChargeUsage(ctx, []model.UsageRecord{charged}, []model.UsageCharge{charge}); err != nil {
				t.Fatalf("charge %d: %v", i, err)
			}
		}
		var billing model.TenantBilling
		if err := db.Where("tenant_id = ?", "tnt_c").Take(&billing).Error; err != nil || billing.BalanceMicro != 900 {
			t.Fatalf("balance = %d (%v), want 900 after one charge", billing.BalanceMicro, err)
		}
		if column, names, _, err := gormstore.PartitionLayoutForTest(db, "usage_charges"); err != nil || column != "" || len(names) != 0 {
			t.Fatalf("usage_charges must stay unpartitioned: %q %v %v", column, names, err)
		}
		var records int64
		db.Model(&model.UsageRecord{}).Where("id = ?", "req_charged").Count(&records)
		if records != 1 {
			t.Fatalf("usage_records rows for the charged request = %d, want 1", records)
		}

		// A retried response turn returns the first row; another tenant's id conflicts.
		// 重试的对话轮次返回第一次写入的行；其它租户占用的 id 返回冲突。
		first := model.ResponseTurn{ID: "resp_dup", TenantID: "tnt_r", Messages: "[]", CreatedAt: at}
		if err := st.CreateResponseTurn(ctx, &first); err != nil {
			t.Fatal(err)
		}
		retry := model.ResponseTurn{ID: "resp_dup", TenantID: "tnt_r", Messages: "[]", CreatedAt: at.Add(time.Second)}
		if err := st.CreateResponseTurn(ctx, &retry); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("retried CreateResponseTurn = %v, want ErrConflict", err)
		}
		other := model.ResponseTurn{ID: "resp_dup", TenantID: "tnt_other", Messages: "[]", CreatedAt: at.Add(2 * time.Second)}
		if err := st.CreateResponseTurn(ctx, &other); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("cross-tenant CreateResponseTurn = %v, want ErrConflict", err)
		}
		got, err := st.GetResponseTurn(ctx, "tnt_r", "resp_dup")
		if err != nil || !got.CreatedAt.Equal(at) {
			t.Fatalf("GetResponseTurn = %+v, %v; want the first write", got, err)
		}
		var turns int64
		db.Model(&model.ResponseTurn{}).Where("id = ?", "resp_dup").Count(&turns)
		if turns != 1 {
			t.Fatalf("response_turns rows = %d, want 1", turns)
		}
	})
}

func catchAllRows(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	query := "SELECT COUNT(*) FROM " + table + " PARTITION (p_max)"
	if db.Name() == "postgres" {
		query = "SELECT COUNT(*) FROM " + table + "_default"
	}
	var n int64
	if err := db.Raw(query).Row().Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLivePartitionMaintenance(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		if res, err := st.MaintainPartitions(ctx, now); err != nil || res.Created != 0 {
			t.Fatalf("MaintainPartitions right after migration = %+v, %v; want nothing to create", res, err)
		}
		_, monthly, _, _ := gormstore.PartitionLayoutForTest(db, "usage_records")

		// Two replicas maintaining at once: never an error, never a duplicate.
		// 两个副本同时维护：既不报错，也不重复建分区。
		later := now.AddDate(0, 0, 10)
		var wg sync.WaitGroup
		results := make([]store.PartitionMaintenanceResult, 2)
		errs := make([]error, 2)
		for i := range results {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				results[i], errs[i] = gormstore.New(db).MaintainPartitions(ctx, later)
			}(i)
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("concurrent maintenance errors: %v, %v", errs[0], errs[1])
		}
		if !results[0].Skipped && !results[1].Skipped {
			if again, err := st.MaintainPartitions(ctx, later); err != nil || again.Created != 0 {
				t.Fatalf("after both passes ran, another pass created %+v (%v)", again, err)
			}
		}
		if _, err := st.MaintainPartitions(ctx, later); err != nil {
			t.Fatal(err)
		}
		_, names, _, _ := gormstore.PartitionLayoutForTest(db, "request_logs")
		seen := map[string]bool{}
		for _, name := range names {
			if seen[name] {
				t.Fatalf("duplicate partition %s", name)
			}
			seen[name] = true
		}
		for d := 0; d <= 17; d++ {
			if !hasPartitionFor(names, dayStart(now).AddDate(0, 0, d)) {
				t.Errorf("request_logs lacks the partition for today+%d: %v", d, names)
			}
		}
		_, monthlyLater, _, _ := gormstore.PartitionLayoutForTest(db, "usage_records")
		if len(monthlyLater) < len(monthly) {
			t.Errorf("usage_records partitions shrank: %v -> %v", monthly, monthlyLater)
		}

		// Rows that reached the catch-all move into the partition built for them.
		// 已进入兜底分区的行会被搬进为它们新建的分区。
		far := now.AddDate(0, 0, 40)
		if err := st.CreateRequestLogs(ctx, []model.RequestLog{{ID: "req_far", TenantID: "tnt_m", KeyDisplay: "k", Endpoint: "chat", StatusCode: 200, Outcome: "ok", CreatedAt: far}}); err != nil {
			t.Fatal(err)
		}
		if got := catchAllRows(t, db, "request_logs"); got != 1 {
			t.Fatalf("catch-all rows = %d, want the far-future row", got)
		}
		if _, err := st.MaintainPartitions(ctx, far); err != nil {
			t.Fatalf("maintenance with rows in the catch-all: %v", err)
		}
		if got := catchAllRows(t, db, "request_logs"); got != 0 {
			t.Errorf("catch-all rows after maintenance = %d, want 0", got)
		}
		page, err := st.ListRequestLogs(ctx, store.ListQuery{}, store.RequestLogFilter{RequestID: "req_far"})
		if err != nil || len(page.Items) != 1 {
			t.Errorf("moved row read = %v, %v", page.Items, err)
		}
		if err := st.CheckSchema(ctx); err != nil {
			t.Fatalf("CheckSchema after maintenance: %v", err)
		}
	})
}

func TestLivePartitionRetention(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st, f, _ := upgradedDatabase(t, ctx, db)
		_, before, _, _ := gormstore.PartitionLayoutForTest(db, "request_logs")

		// Kept at least until the cutoff: every partition wholly before the
		// cutoff's day is dropped, and that day's partition stays whole, so its
		// morning row survives a midday cutoff and nothing is deleted row by row.
		// 至少保留到截止时间：整个区间早于截止日的分区都被删除，截止日的分区整段保留，
		// 因此中午的截止时间不会删掉当天上午的行，也没有逐行删除。
		cutoff := f.day.Add(12 * time.Hour)
		res, err := st.DeleteRequestLogsBefore(ctx, cutoff)
		if err != nil {
			t.Fatal(err)
		}
		if res.RowsDeleted != 0 || res.PartitionsDropped < 2 {
			t.Errorf("retention = %+v, want the history and earlier day partitions dropped and no row deletes", res)
		}
		requestLogIDs := func() []string {
			var ids []string
			for _, row := range snapshot(t, db, "request_logs") {
				ids = append(ids, fmt.Sprint(row["id"]))
			}
			return ids
		}
		if got, want := requestLogIDs(), []string{"req_d_am", "req_d_pm", "req_today"}; !reflect.DeepEqual(got, want) {
			t.Errorf("surviving request logs = %v, want %v", got, want)
		}
		_, after, _, _ := gormstore.PartitionLayoutForTest(db, "request_logs")
		if hasPartitionFor(after, f.day.AddDate(0, 0, -1)) || !hasPartitionFor(after, f.day) {
			t.Errorf("partitions after retention = %v; want the day before dropped and the cutoff's day kept", after)
		}
		if int64(len(before)-len(after)) != res.PartitionsDropped {
			t.Errorf("dropped %d partitions, catalog shrank by %d", res.PartitionsDropped, len(before)-len(after))
		}

		// A late row older than every remaining partition (PostgreSQL routes it
		// to the default partition, MySQL to its lowest partition) is deleted
		// row by row.
		// 早于所有剩余分区的迟到行（PostgreSQL 落入默认分区，MySQL 落入最低分区）按行删除。
		if err := st.CreateRequestLogs(ctx, []model.RequestLog{{ID: "req_late", TenantID: "tnt_p", KeyDisplay: "k", Endpoint: "chat", StatusCode: 200, Outcome: "ok", CreatedAt: f.day.AddDate(0, 0, -50)}}); err != nil {
			t.Fatal(err)
		}
		if res, err := st.DeleteRequestLogsBefore(ctx, cutoff); err != nil || res.RowsDeleted != 1 || res.PartitionsDropped != 0 {
			t.Errorf("stray-row retention = %+v, %v; want exactly the late row deleted", res, err)
		}
		if got, want := requestLogIDs(), []string{"req_d_am", "req_d_pm", "req_today"}; !reflect.DeepEqual(got, want) {
			t.Errorf("request logs after the stray pass = %v, want %v", got, want)
		}

		// Two replicas sweeping at once both succeed and leave nothing expired.
		// 两个副本同时清理都会成功，且不留下过期数据。
		next := f.day.AddDate(0, 0, 1).Add(time.Hour)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, errs[i] = gormstore.New(db).DeleteRequestLogsBefore(ctx, next)
			}(i)
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("concurrent retention errors: %v, %v", errs[0], errs[1])
		}
		var expired int64
		db.Model(&model.RequestLog{}).Where("created_at < ?", dayStart(next)).Count(&expired)
		if expired != 0 {
			t.Errorf("%d expired rows remain after concurrent retention", expired)
		}

		// Reaped turns read back as unknown; audit history ages out too.
		// 被回收的轮次读取时等同未知；审计历史同样会过期清理。
		if res, err := st.DeleteResponseTurnsBefore(ctx, f.day.AddDate(0, 0, 1).Add(time.Hour)); err != nil || res.PartitionsDropped == 0 {
			t.Fatalf("DeleteResponseTurnsBefore = %+v, %v", res, err)
		}
		if _, err := st.GetResponseTurn(ctx, "tnt_p", "resp_old"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("reaped turn read = %v, want ErrNotFound", err)
		}
		if _, err := st.GetResponseTurn(ctx, "tnt_p", "resp_new"); err != nil {
			t.Errorf("kept turn read = %v", err)
		}
		if res, err := st.DeleteAuditLogsBefore(ctx, f.now.AddDate(0, 0, -30)); err != nil || res.PartitionsDropped == 0 {
			t.Fatalf("DeleteAuditLogsBefore = %+v, %v", res, err)
		}
		page, err := st.ListAudit(ctx, "tnt_p", store.ListQuery{}, store.AuditFilter{})
		if err != nil {
			t.Fatal(err)
		}
		var audit []string
		for _, e := range page.Items {
			audit = append(audit, e.ID)
		}
		if strings.Contains(strings.Join(audit, ","), "aud_old") || len(audit) != 2 {
			t.Errorf("audit after retention = %v, want aud_null and aud_recent", audit)
		}
		if res, err := st.DeleteUsageRecordsBefore(ctx, f.now.AddDate(0, 0, -30)); err != nil || res.PartitionsDropped == 0 {
			t.Fatalf("DeleteUsageRecordsBefore = %+v, %v", res, err)
		}
		if res, err := st.DeleteRollupBefore(ctx, f.day); err != nil || res.PartitionsDropped == 0 {
			t.Fatalf("DeleteRollupBefore = %+v, %v", res, err)
		}
		if err := st.CheckSchema(ctx); err != nil {
			t.Fatalf("CheckSchema after retention: %v", err)
		}
	})
}

func TestLivePartitionSchemaCheckNamesTheBrokenTable(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		// Losing the catch-all partition is refused by name.
		// 兜底分区丢失时按表名拒绝。
		drop := "ALTER TABLE response_turns DROP PARTITION p_max"
		if db.Name() == "postgres" {
			drop = "DROP TABLE response_turns_default"
		}
		if err := db.Exec(drop).Error; err != nil {
			t.Fatal(err)
		}
		if err := st.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "response_turns") || !strings.Contains(err.Error(), "catch-all") {
			t.Errorf("CheckSchema without a catch-all = %v, want one naming response_turns", err)
		}
		if db.Name() == "mysql" {
			// An unpartitioned table behind a completed ledger is refused by name.
			// 账本已完成但表未分区时按表名拒绝。
			if err := db.Exec("ALTER TABLE request_logs REMOVE PARTITIONING").Error; err != nil {
				t.Fatal(err)
			}
			if err := st.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "request_logs") {
				t.Errorf("CheckSchema with request_logs unpartitioned = %v, want one naming request_logs", err)
			}
		}
	})
}
