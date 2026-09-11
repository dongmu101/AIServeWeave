package gormstore

import "testing"

func TestLoadJobMigrationsIsOrderedByFilename(t *testing.T) {
	all, err := loadMigrations("jobs", "mysql")
	if err != nil {
		t.Fatalf("loadJobMigrations: %v", err)
	}
	if len(all) < 2 {
		t.Fatalf("len(all) = %d, want at least 2 embedded migrations", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].id >= all[i].id {
			t.Errorf("migrations out of order: %q is not before %q", all[i-1].id, all[i].id)
		}
	}
	for _, m := range all {
		if m.sql == "" {
			t.Errorf("migration %s has an empty body", m.id)
		}
	}
}
