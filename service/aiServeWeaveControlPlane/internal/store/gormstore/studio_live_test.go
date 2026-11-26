package gormstore_test

import (
	"context"
	"testing"

	"gorm.io/gorm"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/storetest"
)

// Studio conversations run the shared contract on both engines.
func TestLiveStudioConversations(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		if err := st.CheckSchema(ctx); err != nil {
			t.Fatal(err)
		}
		storetest.StudioConversations(t, st)
	})
}

// Studio flows and their runs run the shared contract on both engines.
func TestLiveStudioFlows(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		if err := st.CheckSchema(ctx); err != nil {
			t.Fatal(err)
		}
		storetest.StudioFlows(t, st)
	})
}
