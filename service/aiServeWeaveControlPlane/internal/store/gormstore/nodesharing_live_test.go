package gormstore_test

import (
	"AIServeWeave/common/nodeaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
	"gorm.io/gorm"
	"testing"
)

func TestLiveNodeSharingMigrationAndModeChanges(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		st := gormstore.New(db)
		ctx := t.Context()
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		if err := st.CheckSchema(ctx); err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{nodeaccess.ModePrivate, nodeaccess.ModeShared, nodeaccess.ModeDedicated, nodeaccess.ModePrivate} {
			tenants := "[]"
			if mode == nodeaccess.ModeDedicated {
				tenants = `["tenant"]`
			}
			row := model.NodeAccess{NodeID: "agent", Mode: mode, TenantsJSON: tenants, UpdatedBy: "operator"}
			if err := st.PutNodeAccess(ctx, &row); err != nil {
				t.Fatal(err)
			}
			rows, err := st.ListNodeAccess(ctx)
			if err != nil || len(rows) != 1 || rows[0].Mode != mode || rows[0].TenantsJSON != tenants {
				t.Fatal(rows, err)
			}
		}
	})
}
