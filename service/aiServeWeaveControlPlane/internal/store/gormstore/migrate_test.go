package gormstore

import "testing"

func TestBillingSettingsIntegerColumn(t *testing.T) {
	for _, tc := range []struct {
		table, column, kind string
		want                bool
	}{
		{"billing_settings", "id", "int", true},
		{"billing_settings", "id", "integer", true},
		{"billing_settings", "id", "int4", true},
		{"billing_settings", "id", "bigint", true},
		{"billing_settings", "id", "smallint", false},
		{"billing_settings", "id", "varchar", false},
		{"billing_settings", "signup_grant_micro", "int", false},
		{"billing_settings", "signup_grant_micro", "bigint", true},
		{"route_actives", "id", "int", true},
	} {
		if got := compatibleIntegerColumn(tc.table, tc.column, tc.kind); got != tc.want {
			t.Errorf("%s.%s (%s): compatible = %v, want %v", tc.table, tc.column, tc.kind, got, tc.want)
		}
	}
}

func TestMigrationHistoryValidation(t *testing.T) {
	all := []migrationFile{{id: "0001.sql", sql: "SELECT 1"}, {id: "0002.sql", sql: "SELECT 2"}}
	first := migrationRecord{ID: "0001.sql", Checksum: migrationChecksum("SELECT 1")}
	second := migrationRecord{ID: "0002.sql", Checksum: migrationChecksum("SELECT 2")}
	for _, tt := range []struct {
		name    string
		records []migrationRecord
		resume  bool
		wantErr bool
	}{
		{name: "empty"},
		{name: "applied prefix", records: []migrationRecord{first}},
		{name: "complete", records: []migrationRecord{first, second}},
		{name: "hole", records: []migrationRecord{second}, wantErr: true},
		{name: "unknown", records: []migrationRecord{{ID: "9999.sql"}}, wantErr: true},
		{name: "modified", records: []migrationRecord{{ID: first.ID, Checksum: "wrong"}}, wantErr: true},
		{name: "cleared checksum", records: []migrationRecord{{ID: first.ID}}, wantErr: true},
		{name: "dirty", records: []migrationRecord{{ID: first.ID, Checksum: first.Checksum, Dirty: true}}, wantErr: true},
		{name: "explicit resume", records: []migrationRecord{{ID: first.ID, Checksum: first.Checksum, Dirty: true}}, resume: true},
		{name: "resume still rejects modification", records: []migrationRecord{{ID: first.ID, Checksum: "wrong", Dirty: true}}, resume: true, wantErr: true},
		{name: "later version after dirty", records: []migrationRecord{{ID: first.ID, Checksum: first.Checksum, Dirty: true}, second}, resume: true, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMigrationHistory(all, tt.records, tt.resume)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateMigrationHistory() = %v, want error %v", err, tt.wantErr)
			}
		})
	}
}
