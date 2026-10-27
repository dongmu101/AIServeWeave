package gormstore_test

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
)

func TestLiveMetricsHistoryUpsertIsIdempotentPerBucket(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, dialect string) {
		st := gormstore.New(db)
		ctx := context.Background()
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatalf("MigrateAll() error = %v", err)
		}
		bucket := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
		point := model.MetricsHistoryPoint{Metric: "gateway_http_requests_total", Labels: "endpoint=chat,status=200", BucketAt: bucket, Value: 10}

		if err := st.UpsertRollup(ctx, []model.MetricsHistoryPoint{point}); err != nil {
			t.Fatalf("UpsertRollup() first write error = %v", err)
		}
		point.Value = 15 // simulate a second scrape of the same bucket, counter advanced
		if err := st.UpsertRollup(ctx, []model.MetricsHistoryPoint{point}); err != nil {
			t.Fatalf("UpsertRollup() second write error = %v", err)
		}

		got, err := st.ListRollup(ctx, []string{"gateway_http_requests_total"}, bucket.Add(-time.Minute), bucket.Add(time.Minute))
		if err != nil {
			t.Fatalf("ListRollup() error = %v", err)
		}
		if len(got) != 1 || got[0].Value != 15 {
			t.Fatalf("ListRollup() = %+v, want exactly one row with the updated value 15", got)
		}
	})
}

func TestLiveMetricsHistoryDeleteRollupBefore(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, dialect string) {
		st := gormstore.New(db)
		ctx := context.Background()
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatalf("MigrateAll() error = %v", err)
		}
		old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		recent := time.Now().UTC().Truncate(time.Second)
		if err := st.UpsertRollup(ctx, []model.MetricsHistoryPoint{
			{Metric: "m", Labels: "", BucketAt: old, Value: 1},
			{Metric: "m", Labels: "", BucketAt: recent, Value: 2},
		}); err != nil {
			t.Fatalf("UpsertRollup() error = %v", err)
		}

		n, err := st.DeleteRollupBefore(ctx, recent.Add(-time.Hour))
		if err != nil {
			t.Fatalf("DeleteRollupBefore() error = %v", err)
		}
		if n != 1 {
			t.Fatalf("DeleteRollupBefore() removed %d rows, want 1", n)
		}
	})
}
