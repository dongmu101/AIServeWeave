package gormstore_test

import (
	"context"
	"testing"

	"gorm.io/gorm"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/storetest"
)

// TestLiveUsageQueries runs the shared usage query scenario on the
// partitioned usage_records of both databases.
func TestLiveUsageQueries(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		st := gormstore.New(db)
		if err := st.MigrateAll(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		storetest.UsageQueries(t, st)
	})
}
