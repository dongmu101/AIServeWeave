package gormstore

import "testing"

func TestLoadJobMigrationsIsOrderedByFilename(t *testing.T) {
	all, err := loadJobMigrations()
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

func TestPendingJobMigrationsSkipsAppliedAndPreservesOrder(t *testing.T) {
	all := []migrationFile{
		{id: "0001_a.sql", sql: "A"},
		{id: "0002_b.sql", sql: "B"},
		{id: "0003_c.sql", sql: "C"},
	}

	tests := []struct {
		name    string
		applied map[string]bool
		want    []string
	}{
		{
			name:    "nothing applied yet",
			applied: map[string]bool{},
			want:    []string{"0001_a.sql", "0002_b.sql", "0003_c.sql"},
		},
		{
			name:    "the first migration already applied",
			applied: map[string]bool{"0001_a.sql": true},
			want:    []string{"0002_b.sql", "0003_c.sql"},
		},
		{
			name:    "every migration already applied",
			applied: map[string]bool{"0001_a.sql": true, "0002_b.sql": true, "0003_c.sql": true},
			want:    nil,
		},
		{
			name:    "a hole in the middle is still returned — order is by filename, not by what is missing",
			applied: map[string]bool{"0001_a.sql": true, "0003_c.sql": true},
			want:    []string{"0002_b.sql"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pendingJobMigrations(all, tt.applied)
			if len(got) != len(tt.want) {
				t.Fatalf("pendingJobMigrations() = %v, want %v", ids(got), tt.want)
			}
			for i, m := range got {
				if m.id != tt.want[i] {
					t.Errorf("pendingJobMigrations()[%d] = %q, want %q", i, m.id, tt.want[i])
				}
			}
		})
	}
}

func ids(ms []migrationFile) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.id
	}
	return out
}
