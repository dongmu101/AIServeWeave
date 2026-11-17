package gormstore_test

import (
	"context"
	"testing"

	"gorm.io/gorm"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/storetest"
)

// TestLiveChargeUsage runs the shared charging scenario on the partitioned
// usage_records of both databases.
func TestLiveChargeUsage(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		storetest.ChargeUsage(t, st)
	})
}
