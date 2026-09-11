package gormstore_test

import (
	"context"
	"testing"

	"gorm.io/gorm"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
)

// TestLiveMetricsHistoryMigrationIsRepeatable proves the metrics_history
// namespace migrates cleanly on both engines and that a second MigrateAll
// against an already-current database is a no-op, matching every other
// namespace's contract.
//
// TestLiveMetricsHistoryMigrationIsRepeatable 证明 metrics_history 命名空间
// 在两个引擎上都能干净地迁移，且对一个已是最新状态的数据库再跑一次 MigrateAll
// 是空操作，与其它每个命名空间的约定一致。
func TestLiveMetricsHistoryMigrationIsRepeatable(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, dialect string) {
		st := gormstore.New(db)
		ctx := context.Background()
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatalf("MigrateAll() first run error = %v", err)
		}
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatalf("MigrateAll() second run error = %v, want nil (a no-op against an already-current database)", err)
		}
		if !db.Migrator().HasTable("metrics_history_points") {
			t.Fatal("metrics_history_points table was not created")
		}
	})
}
