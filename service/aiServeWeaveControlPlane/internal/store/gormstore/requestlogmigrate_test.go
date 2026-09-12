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
