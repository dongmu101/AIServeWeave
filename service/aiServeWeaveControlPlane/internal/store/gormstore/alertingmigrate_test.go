package gormstore

import "testing"

func TestAlertingNamespaceIsRegisteredForBothDialects(t *testing.T) {
	tests := []struct {
		name    string
		dialect string
	}{
		{name: "postgres includes alerting", dialect: "postgres"},
		{name: "mysql includes alerting", dialect: "mysql"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groups := migrationNamespaces(tt.dialect)
			found := false
			for _, g := range groups {
				if g == "alerting" {
					found = true
				}
			}
			if !found {
				t.Fatalf("migrationNamespaces(%q) = %v, want it to include \"alerting\"", tt.dialect, groups)
			}
		})
	}
}

func TestLoadAlertingMigrationsParsesEmbeddedSQL(t *testing.T) {
	for _, dialect := range []string{"postgres", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			files, err := loadMigrations("alerting", dialect)
			if err != nil {
				t.Fatalf("loadMigrations(alerting, %s) error = %v, want nil", dialect, err)
			}
			if len(files) != 2 {
				t.Fatalf("loadMigrations(alerting, %s) returned %d files, want 2 (alert_rules + alert_instances)", dialect, len(files))
			}
		})
	}
}
