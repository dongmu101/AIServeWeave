package gormstore_test

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
)

func TestLiveRequestLogsCreateIsIdempotentOnDuplicateID(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, dialect string) {
		st := gormstore.New(db)
		ctx := context.Background()
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatalf("MigrateAll() error = %v", err)
		}

		record := model.RequestLog{
			ID: "req_dup_1", TenantID: "tnt_1", KeyDisplay: "aisw-abcd1234",
			Endpoint: "chat", StatusCode: 200, Outcome: "ok", DurationMS: 42,
			CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
		}
		if err := st.CreateRequestLogs(ctx, []model.RequestLog{record}); err != nil {
			t.Fatalf("first CreateRequestLogs() error = %v, want nil", err)
		}
		// A retried push carrying the same id must not error and must not
		// duplicate the row.
		if err := st.CreateRequestLogs(ctx, []model.RequestLog{record}); err != nil {
			t.Fatalf("duplicate CreateRequestLogs() error = %v, want nil (silently skipped)", err)
		}

		page, err := st.ListRequestLogs(ctx, store.ListQuery{}, store.RequestLogFilter{TenantID: "tnt_1"})
		if err != nil {
			t.Fatalf("ListRequestLogs() error = %v, want nil", err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("ListRequestLogs() returned %d items, want exactly 1 despite the duplicate push", len(page.Items))
		}
	})
}

func TestLiveRequestLogsFiltersAndPaginatesByTenant(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, dialect string) {
		st := gormstore.New(db)
		ctx := context.Background()
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatalf("MigrateAll() error = %v", err)
		}

		base := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
		records := []model.RequestLog{
			{ID: "req_a1", TenantID: "tnt_a", KeyDisplay: "aisw-a", Endpoint: "chat", StatusCode: 200, Outcome: "ok", DurationMS: 10, CreatedAt: base},
			{ID: "req_a2", TenantID: "tnt_a", KeyDisplay: "aisw-a", Endpoint: "chat", StatusCode: 429, Outcome: "rate_limited", DurationMS: 5, CreatedAt: base.Add(time.Minute)},
			{ID: "req_b1", TenantID: "tnt_b", KeyDisplay: "aisw-b", Endpoint: "embeddings", StatusCode: 200, Outcome: "ok", DurationMS: 8, CreatedAt: base.Add(2 * time.Minute)},
		}
		if err := st.CreateRequestLogs(ctx, records); err != nil {
			t.Fatalf("CreateRequestLogs() error = %v, want nil", err)
		}

		page, err := st.ListRequestLogs(ctx, store.ListQuery{}, store.RequestLogFilter{TenantID: "tnt_a"})
		if err != nil {
			t.Fatalf("ListRequestLogs(tnt_a) error = %v, want nil", err)
		}
		if len(page.Items) != 2 {
			t.Fatalf("ListRequestLogs(tnt_a) returned %d items, want 2 (tnt_b's row must not leak in)", len(page.Items))
		}

		page, err = st.ListRequestLogs(ctx, store.ListQuery{}, store.RequestLogFilter{Outcome: "rate_limited"})
		if err != nil {
			t.Fatalf("ListRequestLogs(outcome=rate_limited) error = %v, want nil", err)
		}
		if len(page.Items) != 1 || page.Items[0].ID != "req_a2" {
			t.Fatalf("ListRequestLogs(outcome=rate_limited) = %+v, want exactly req_a2", page.Items)
		}
	})
}

func TestLiveRequestLogsDeleteBeforeRemovesOnlyOlderRows(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, dialect string) {
		st := gormstore.New(db)
		ctx := context.Background()
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatalf("MigrateAll() error = %v", err)
		}

		old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		recent := time.Now().UTC().Truncate(time.Millisecond)
		records := []model.RequestLog{
			{ID: "req_old", TenantID: "tnt_1", KeyDisplay: "aisw-a", Endpoint: "chat", StatusCode: 200, Outcome: "ok", DurationMS: 1, CreatedAt: old},
			{ID: "req_recent", TenantID: "tnt_1", KeyDisplay: "aisw-a", Endpoint: "chat", StatusCode: 200, Outcome: "ok", DurationMS: 1, CreatedAt: recent},
		}
		if err := st.CreateRequestLogs(ctx, records); err != nil {
			t.Fatalf("CreateRequestLogs() error = %v, want nil", err)
		}

		n, err := st.DeleteRequestLogsBefore(ctx, recent.Add(-time.Hour))
		if err != nil {
			t.Fatalf("DeleteRequestLogsBefore() error = %v, want nil", err)
		}
		if n != 1 {
			t.Fatalf("DeleteRequestLogsBefore() removed %d rows, want 1", n)
		}

		page, err := st.ListRequestLogs(ctx, store.ListQuery{}, store.RequestLogFilter{TenantID: "tnt_1"})
		if err != nil {
			t.Fatalf("ListRequestLogs() error = %v, want nil", err)
		}
		if len(page.Items) != 1 || page.Items[0].ID != "req_recent" {
			t.Fatalf("ListRequestLogs() = %+v, want exactly req_recent to survive", page.Items)
		}
	})
}
