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
			if len(files) != 1 {
				t.Fatalf("loadMigrations(response_turns, %s) returned %d files, want 1", dialect, len(files))
			}
		})
	}
}
