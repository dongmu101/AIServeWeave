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
			// 0001 creates the table; 0002 adds the plain (created_at, id) index
			// that the operator cross-tenant search and the retention sweep
			// need, since neither filters by tenant_id.
			//
			// 0001 建表；0002 补上一个纯 (created_at, id) 索引，供跨租户的
			// 运维检索与保留期清理使用——两者都不按 tenant_id 过滤。
			if len(files) != 2 {
				t.Fatalf("loadMigrations(request_logs, %s) returned %d files, want 2", dialect, len(files))
			}
		})
	}
}
