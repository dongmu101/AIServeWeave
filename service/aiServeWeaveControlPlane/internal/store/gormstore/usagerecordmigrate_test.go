package gormstore

import "testing"

func TestUsageRecordsNamespaceIsRegisteredForBothDialects(t *testing.T) {
	tests := []struct {
		name    string
		dialect string
	}{
		{name: "postgres includes usage_records", dialect: "postgres"},
		{name: "mysql includes usage_records", dialect: "mysql"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groups := migrationNamespaces(tt.dialect)
			found := false
			for _, g := range groups {
				if g == "usage_records" {
					found = true
				}
			}
			if !found {
				t.Fatalf("migrationNamespaces(%q) = %v, want it to include \"usage_records\"", tt.dialect, groups)
			}
		})
	}
}

func TestLoadUsageRecordsMigrationsParsesEmbeddedSQL(t *testing.T) {
	for _, dialect := range []string{"postgres", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			files, err := loadMigrations("usage_records", dialect)
			if err != nil {
				t.Fatalf("loadMigrations(usage_records, %s) error = %v, want nil", dialect, err)
			}
			// 0001 creates the table; 0002 converts it to time partitions; 0003
			// adds key_id and user_id; 0004 widens the id columns to hold UUIDv7
			// ids; 0005 adds the meter breakdown and pricing columns; 0006 adds
			// the (tenant_id, created_at, id) index the per-request list walks;
			// 0007 adds schema comments.
			// 0001 建表；0002 把它转换为按时间分区；0003 新增 key_id 与 user_id；0004
			// 加宽 id 列以容纳 UUIDv7 id；0005 新增计量项细分与计价列；0006 新增逐笔列表所用的
			// (tenant_id, created_at, id) 索引；0007 添加表和字段注释。
			if len(files) != 7 {
				t.Fatalf("loadMigrations(usage_records, %s) returned %d files, want 7", dialect, len(files))
			}
		})
	}
}
