package gormstore

import (
	"fmt"
	"os"
	"path"
	"reflect"
	"strings"
	"testing"
)

// migrationDir mirrors loadMigrations' directory layout so the tests below read
// the same embedded files the migrator runs.
//
// migrationDir 复刻 loadMigrations 的目录布局，使下面的测试读取与迁移器执行相同的内嵌文件。
func migrationDir(namespace, dialect string) string {
	switch namespace {
	case "jobs":
		return "migrations/jobs"
	case "workflow_templates":
		return "migrations/workflowtemplates/" + dialect
	}
	return "migrations/" + namespace + "/" + dialect
}

// TestMigrationFilesAreWellFormed asserts every namespace has the same version
// filenames on PostgreSQL and MySQL (a version added to one dialect only would
// leave the other without its schema change), that names are strictly
// ordered and contiguous, and that no body is empty.
//
// TestMigrationFilesAreWellFormed 断言每个分组在 PostgreSQL 与 MySQL 下的版本文件名一致
// （只给一种方言新增版本，会让另一种方言缺失对应的表结构变更），文件名严格有序且连续，
// 且没有空文件。
func TestMigrationFilesAreWellFormed(t *testing.T) {
	for _, namespace := range migrationNamespaces("mysql") {
		names := map[string][]string{}
		for _, dialect := range []string{"mysql", "postgres"} {
			if namespace == "jobs" && dialect == "postgres" {
				continue
			}
			all, err := loadMigrations(namespace, dialect)
			if err != nil {
				t.Fatalf("%s/%s: %v", namespace, dialect, err)
			}
			for i, m := range all {
				if strings.TrimSpace(m.sql) == "" {
					t.Errorf("%s/%s/%s is empty", namespace, dialect, m.id)
				}
				if i > 0 && all[i-1].id >= m.id {
					t.Errorf("%s/%s: %s is not after %s", namespace, dialect, m.id, all[i-1].id)
				}
				if want := fmt.Sprintf("%04d_", i+1); !strings.HasPrefix(m.id, want) {
					t.Errorf("%s/%s: %s should carry version %s (versions must be contiguous)", namespace, dialect, m.id, want)
				}
				names[dialect] = append(names[dialect], m.id)
			}
		}
		if namespace != "jobs" && !reflect.DeepEqual(names["mysql"], names["postgres"]) {
			t.Errorf("%s: mysql and postgres version files differ:\nmysql    %v\npostgres %v", namespace, names["mysql"], names["postgres"])
		}
	}
}

// TestPaginationIndexMigrationsKeepDialectsAligned asserts the keyset-pagination
// indexes exist in both dialects with identical definitions, and that every
// statement is shaped the way executeMigration's replay check for the base
// namespace recognises (CREATE INDEX <name> ON <table> ...), so an interrupted
// MySQL run can be resumed without a duplicate-index failure.
//
// TestPaginationIndexMigrationsKeepDialectsAligned 断言游标分页索引在两种方言下都存在且定义
// 一致，且每条语句都符合 executeMigration 对 base 分组的重放判断所能识别的形状
// （CREATE INDEX <name> ON <table> ...），使中断的 MySQL 迁移可以续跑而不因重复索引失败。
func TestPaginationIndexMigrationsKeepDialectsAligned(t *testing.T) {
	want := map[string]string{
		"idx_users_tenant_created":      "users (tenant_id, created_at, id)",
		"idx_api_keys_tenant_created":   "api_keys (tenant_id, created_at, id)",
		"idx_audit_logs_tenant_created": "audit_logs (tenant_id, created_at, id)",
	}
	for _, dialect := range []string{"mysql", "postgres"} {
		body, err := migrationsFS.ReadFile("migrations/base/" + dialect + "/0016_list_pagination_indexes.sql")
		if err != nil {
			t.Fatalf("%s: %v", dialect, err)
		}
		got := map[string]string{}
		for _, statement := range strings.Split(string(body), ";") {
			words := strings.Fields(statement)
			if len(words) == 0 {
				continue
			}
			if len(words) < 6 || words[0] != "CREATE" || words[1] != "INDEX" || words[3] != "ON" {
				t.Fatalf("%s: statement %q is not CREATE INDEX <name> ON <table> (...)", dialect, statement)
			}
			got[words[2]] = strings.Join(words[4:], " ")
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s indexes = %v, want %v", dialect, got, want)
		}
	}
	body, err := migrationsFS.ReadFile("migrations/jobs/0005_add_job_artifacts_type_created_index.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "idx_job_artifacts_type_created (type, created_at, id)") {
		t.Errorf("jobs/0005 does not create idx_job_artifacts_type_created (type, created_at, id): %q", body)
	}
}

// initSQLPath locates sql/init.sql relative to this package directory.
//
// initSQLPath 相对本包目录定位 sql/init.sql。
const initSQLPath = "../../../../../sql/init.sql"

// renderInitSQL concatenates the embedded MySQL migrations in migrationNamespaces
// order under the hand-written header already at the top of sql/init.sql (every
// line before the first "-- ====" banner).
//
// renderInitSQL 按 migrationNamespaces 的顺序，把内嵌的 MySQL 迁移拼接在 sql/init.sql
// 顶部已有的手写文件头（第一个 "-- ====" 横幅之前的全部行）之下。
func renderInitSQL(t *testing.T, current string) string {
	t.Helper()
	const banner = "-- ============================================================\n"
	header, _, found := strings.Cut(current, banner)
	if !found {
		t.Fatal("sql/init.sql has no namespace banner to anchor the header on")
	}
	var out strings.Builder
	out.WriteString(header)
	for i, namespace := range migrationNamespaces("mysql") {
		if i > 0 {
			out.WriteString("\n")
		}
		out.WriteString(banner + "-- " + namespace + "\n" + banner)
		all, err := loadMigrations(namespace, "mysql")
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range all {
			out.WriteString("\n-- " + strings.TrimPrefix(path.Join(migrationDir(namespace, "mysql"), m.id), "migrations/") + "\n")
			// Partition bounds are only known at apply time; the reference
			// script keeps just the catch-all partition, and a real deployment
			// gets its dated partitions from the migration and maintenance.
			// 分区边界只有执行时才能确定；参考脚本只保留兜底分区，真实部署的按日期分区
			// 由迁移与分区维护生成。
			body := partitionDirective.ReplaceAllString(strings.TrimRight(m.sql, "\n"), mysqlCatchAllDef)
			// The migrator splits on ";" and never needs a final one, but the
			// concatenated script does.
			// 迁移器按 ";" 切分，不需要末尾分号；拼接后的脚本需要。
			if !strings.HasSuffix(body, ";") {
				body += ";"
			}
			out.WriteString(body + "\n")
		}
	}
	return out.String()
}

// TestInitSQLMatchesMigrations asserts sql/init.sql is exactly the concatenation
// of the embedded MySQL migrations. After adding a migration, regenerate it with
//
//	UPDATE_INIT_SQL=1 go test ./service/aiServeWeaveControlPlane/internal/store/gormstore -run TestInitSQLMatchesMigrations
//
// TestInitSQLMatchesMigrations 断言 sql/init.sql 恰好是内嵌 MySQL 迁移的拼接结果。新增迁移后，
// 用上面的 UPDATE_INIT_SQL=1 命令重新生成。
func TestInitSQLMatchesMigrations(t *testing.T) {
	raw, err := os.ReadFile(initSQLPath)
	if err != nil {
		t.Fatalf("reading sql/init.sql: %v", err)
	}
	want := renderInitSQL(t, string(raw))
	if os.Getenv("UPDATE_INIT_SQL") == "1" {
		if err := os.WriteFile(initSQLPath, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if string(raw) != want {
		t.Fatal("sql/init.sql is out of date with the embedded migrations; regenerate with UPDATE_INIT_SQL=1 go test ./service/aiServeWeaveControlPlane/internal/store/gormstore -run TestInitSQLMatchesMigrations")
	}
}
