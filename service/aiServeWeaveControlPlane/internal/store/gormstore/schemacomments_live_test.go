package gormstore_test

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode"

	"gorm.io/gorm"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
)

func TestLiveSchemaComments(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}

		query := `
SELECT TABLE_NAME AS object_name, TABLE_COMMENT AS description
FROM information_schema.TABLES
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'
UNION ALL
SELECT CONCAT(TABLE_NAME, '.', COLUMN_NAME), COLUMN_COMMENT
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE()
`
		if db.Name() == "postgres" {
			// Advance the clock so the check includes partitions created after migration.
			result, err := st.MaintainPartitions(ctx, time.Now().UTC().AddDate(0, 0, 2))
			if err != nil || result.Created == 0 {
				t.Fatalf("maintaining future partitions: result = %+v, error = %v", result, err)
			}
			query = `
SELECT c.relname AS object_name, COALESCE(obj_description(c.oid, 'pg_class'), '') AS description
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p')
UNION ALL
SELECT c.relname || '.' || a.attname, COALESCE(col_description(c.oid, a.attnum), '')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p')
`
		}
		var rows []struct {
			ObjectName  string
			Description string
		}
		if err := db.Raw(query).Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			t.Fatal("database catalog returned no tables or columns")
		}
		for _, row := range rows {
			if !strings.ContainsFunc(row.Description, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
				t.Errorf("%s lacks a Chinese comment: %q", row.ObjectName, row.Description)
			}
		}
	})
}
