package gormstore

import "testing"

func TestResponseTurnsNamespaceIsRegisteredForBothDialects(t *testing.T) {
	tests := []struct {
		name    string
		dialect string
	}{
		{name: "postgres includes response_turns", dialect: "postgres"},
		{name: "mysql includes response_turns", dialect: "mysql"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groups := migrationNamespaces(tt.dialect)
			found := false
			for _, g := range groups {
				if g == "response_turns" {
					found = true
				}
			}
			if !found {
				t.Fatalf("migrationNamespaces(%q) = %v, want it to include \"response_turns\"", tt.dialect, groups)
			}
		})
	}
}

func TestLoadResponseTurnsMigrationsParsesEmbeddedSQL(t *testing.T) {
	for _, dialect := range []string{"postgres", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			files, err := loadMigrations("response_turns", dialect)
			if err != nil {
				t.Fatalf("loadMigrations(response_turns, %s) error = %v, want nil", dialect, err)
			}
			// 0001 creates the table; 0002 converts it to time partitions; 0003
			// widens tenant_id to hold UUIDv7 ids; 0004 adds schema comments.
			// 0001 建表；0002 按时间分区；0003 加宽 tenant_id 以容纳 UUIDv7 id；0004 添加表和字段注释。
			if len(files) != 4 {
				t.Fatalf("loadMigrations(response_turns, %s) returned %d files, want 4", dialect, len(files))
			}
		})
	}
}
