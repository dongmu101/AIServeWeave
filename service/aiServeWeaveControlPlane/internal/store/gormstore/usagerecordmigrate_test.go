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
			if len(files) != 1 {
				t.Fatalf("loadMigrations(usage_records, %s) returned %d files, want 1", dialect, len(files))
			}
		})
	}
}
