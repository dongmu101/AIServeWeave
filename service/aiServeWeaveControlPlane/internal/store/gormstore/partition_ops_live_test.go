package gormstore_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
)

// The tests below cover how partitioning behaves operationally: a runtime
// account without DDL rights, a separate maintenance account, the periodic
// catch-all move limit and the MySQL session setting partition DDL changes.
//
// 以下测试覆盖分区在运维层面的行为：没有 DDL 权限的运行账号、单独的维护账号、定时维护的
// 兜底分区搬移上限，以及分区 DDL 对 MySQL 会话设置的修改。

// openAs opens the test database dsn as another account.
//
// openAs 以另一个账号打开测试数据库 dsn。
func openAs(t *testing.T, db *gorm.DB, dsn, user, password string, maxOpen int) *gorm.DB {
	t.Helper()
	var dialect gorm.Dialector
	if db.Name() == "mysql" {
		cfg, err := mysqldriver.ParseDSN(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg.User, cfg.Passwd = user, password
		dialect = mysql.Open(cfg.FormatDSN())
	} else {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		u.User = url.UserPassword(user, password)
		dialect = postgres.Open(u.String())
	}
	other, err := gorm.Open(dialect, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), TranslateError: true})
	if err != nil {
		t.Fatalf("opening as %s: %v", user, err)
	}
	pool, err := other.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(maxOpen)
	t.Cleanup(func() { _ = pool.Close() })
	return other
}

// restrictedAccount creates an account that may read and write every table
// but run no DDL, like a production runtime account, and opens it.
//
// restrictedAccount 创建一个可以读写所有表、但不能执行 DDL 的账号（与生产运行账号相同），
// 并以它打开连接。
func restrictedAccount(t *testing.T, db *gorm.DB, dsn string) *gorm.DB {
	t.Helper()
	var database string
	query := "SELECT DATABASE()"
	if db.Name() == "postgres" {
		query = "SELECT current_database()"
	}
	if err := db.Raw(query).Row().Scan(&database); err != nil {
		t.Fatal(err)
	}
	user := "nodl_" + database[len(database)-10:]
	const password = "restricted-test-pw"
	if db.Name() == "mysql" {
		for _, statement := range []string{
			fmt.Sprintf("CREATE USER '%s'@'%%' IDENTIFIED BY '%s'", user, password),
			fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON %s.* TO '%s'@'%%'", database, user),
		} {
			if err := db.Exec(statement).Error; err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() { db.Exec(fmt.Sprintf("DROP USER '%s'@'%%'", user)) })
	} else {
		for _, statement := range []string{
			fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", user, password),
			fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %s", user),
			fmt.Sprintf("GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO %s", user),
		} {
			if err := db.Exec(statement).Error; err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() {
			db.Exec("DROP OWNED BY " + user)
			db.Exec("DROP ROLE " + user)
		})
	}
	return openAs(t, db, dsn, user, password, 4)
}

func TestLivePartitionRetentionFallsBackWithoutDDLRights(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, dsn string) {
		ctx := context.Background()
		_, f, _ := upgradedDatabase(t, ctx, db)
		restore := gormstore.SetPartitionTuningForTest(1, 0, nil)
		defer restore()
		// Partition DDL runs on an account that may not alter the tables.
		// 分区 DDL 在一个无权修改这些表的账号上执行。
		st := gormstore.New(db, gormstore.WithMaintenanceDB(restrictedAccount(t, db, dsn)))

		if _, err := st.MaintainPartitions(ctx, f.now.AddDate(0, 0, 20)); err == nil {
			t.Error("MaintainPartitions without DDL rights succeeded, want an error to surface")
		}
		// Retention still happens, row by row in batches, and says why.
		// 保留期清理照常进行，改为分批逐行删除，并说明原因。
		res, err := st.DeleteRequestLogsBefore(ctx, f.day.Add(12*time.Hour))
		if err == nil || !strings.Contains(err.Error(), "row by row") {
			t.Errorf("fallback error = %v, want one explaining the row-by-row fallback", err)
		}
		if res.PartitionsDropped != 0 || res.RowsDeleted != 2 {
			t.Errorf("fallback = %+v, want the two expired rows deleted one batch at a time", res)
		}
		var expired int64
		db.Model(&model.RequestLog{}).Where("created_at < ?", f.day).Count(&expired)
		if expired != 0 {
			t.Errorf("%d expired rows remain after the fallback", expired)
		}
	})
}

func TestLivePartitionMaintenanceAccountServesARestrictedRuntime(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, dsn string) {
		ctx := context.Background()
		_, f, _ := upgradedDatabase(t, ctx, db)
		// The runtime pool cannot run DDL; the maintenance pool (the owner) can.
		// 运行连接池不能执行 DDL；维护连接池（表的所有者）可以。
		runtime := restrictedAccount(t, db, dsn)
		st := gormstore.New(runtime, gormstore.WithMaintenanceDB(db))

		later := f.now.AddDate(0, 0, 20)
		if res, err := st.MaintainPartitions(ctx, later); err != nil || res.Created == 0 {
			t.Fatalf("MaintainPartitions through the maintenance pool = %+v, %v", res, err)
		}
		_, names, _, _ := gormstore.PartitionLayoutForTest(db, "request_logs")
		if !hasPartitionFor(names, dayStart(later).AddDate(0, 0, 7)) {
			t.Errorf("request_logs partitions %v do not reach later + 7 days", names)
		}
		res, err := st.DeleteRequestLogsBefore(ctx, f.day.Add(12*time.Hour))
		if err != nil || res.PartitionsDropped < 2 || res.RowsDeleted != 0 {
			t.Fatalf("retention through the maintenance pool = %+v, %v; want whole partitions dropped", res, err)
		}
		// The restricted runtime account still reads and writes, including rows
		// landing in partitions the maintenance account created.
		// 受限的运行账号仍能读写，包括落进维护账号新建分区的行。
		if err := st.CreateRequestLogs(ctx, []model.RequestLog{{ID: "req_runtime", TenantID: "tnt_p", KeyDisplay: "k", Endpoint: "chat", StatusCode: 200, Outcome: "ok", CreatedAt: later}}); err != nil {
			t.Fatalf("runtime write into a maintenance-created partition: %v", err)
		}
		page, err := st.ListRequestLogs(ctx, store.ListQuery{}, store.RequestLogFilter{RequestID: "req_runtime"})
		if err != nil || len(page.Items) != 1 {
			t.Errorf("runtime read = %v, %v", page.Items, err)
		}
	})
}

func TestLivePartitionLargeCatchAllBacklogWaitsForTheMigrationCommand(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		far := time.Now().UTC().AddDate(0, 0, 40)
		restore := gormstore.SetPartitionTuningForTest(0, 2, func() time.Time { return far })
		defer restore()
		var rows []model.RequestLog
		for i := 0; i < 3; i++ {
			rows = append(rows, model.RequestLog{ID: fmt.Sprintf("req_backlog_%d", i), TenantID: "tnt_b", KeyDisplay: "k", Endpoint: "chat", StatusCode: 200, Outcome: "ok", CreatedAt: far.Add(time.Duration(i) * time.Minute)})
		}
		if err := st.CreateRequestLogs(ctx, rows); err != nil {
			t.Fatal(err)
		}
		// The periodic pass refuses a backlog above its limit rather than
		// blocking writes while it moves it.
		// 定时维护遇到超过上限的积压时拒绝搬移，而不是在搬移期间阻塞写入。
		if _, err := st.MaintainPartitions(ctx, far); err == nil || !strings.Contains(err.Error(), "-migrate up") {
			t.Fatalf("MaintainPartitions over the move limit = %v, want a pointer to -migrate up", err)
		}
		if got := catchAllRows(t, db, "request_logs"); got != 3 {
			t.Fatalf("catch-all rows = %d, want the backlog untouched", got)
		}
		// The migration command moves it with no limit.
		// 迁移命令不受上限约束，会完成搬移。
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatalf("MigrateAll: %v", err)
		}
		if got := catchAllRows(t, db, "request_logs"); got != 0 {
			t.Errorf("catch-all rows after the migration command = %d, want 0", got)
		}
	})
}

func TestLivePartitionDDLRestoresTheMySQLSessionLockWait(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, dsn string) {
		if db.Name() != "mysql" {
			t.Skip("lock_wait_timeout is a MySQL session setting")
		}
		ctx := context.Background()
		if err := gormstore.New(db).MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		cfg, err := mysqldriver.ParseDSN(dsn)
		if err != nil {
			t.Fatal(err)
		}
		// One connection, so the session the DDL ran on is the one checked after.
		// 只有一个连接，因此之后检查的正是执行过 DDL 的会话。
		single := openAs(t, db, dsn, cfg.User, cfg.Passwd, 1)
		st := gormstore.New(db, gormstore.WithMaintenanceDB(single))
		if res, err := st.MaintainPartitions(ctx, time.Now().UTC().AddDate(0, 0, 5)); err != nil || res.Created == 0 {
			t.Fatalf("MaintainPartitions = %+v, %v; want partition DDL to run", res, err)
		}
		var session, global int64
		if err := single.Raw("SELECT @@SESSION.lock_wait_timeout, @@GLOBAL.lock_wait_timeout").Row().Scan(&session, &global); err != nil {
			t.Fatal(err)
		}
		if session != global {
			t.Errorf("pooled session lock_wait_timeout = %d after partition DDL, want the default %d", session, global)
		}
	})
}
