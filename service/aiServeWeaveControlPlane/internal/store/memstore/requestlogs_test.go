package memstore_test

import (
	"context"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
)

func TestCreateRequestLogsSkipsADuplicateIDSilently(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	rec := model.RequestLog{ID: "req_1", TenantID: "tnt_1", Endpoint: "chat", StatusCode: 200, Outcome: "ok", CreatedAt: time.Now()}

	if err := s.CreateRequestLogs(ctx, []model.RequestLog{rec}); err != nil {
		t.Fatalf("first CreateRequestLogs() error = %v, want nil", err)
	}
	dup := rec
	dup.StatusCode = 500 // a different payload under the same id must not overwrite
	if err := s.CreateRequestLogs(ctx, []model.RequestLog{dup}); err != nil {
		t.Fatalf("duplicate CreateRequestLogs() error = %v, want nil (silent skip, not an error)", err)
	}

	page, err := s.ListRequestLogs(ctx, store.ListQuery{}, store.RequestLogFilter{TenantID: "tnt_1"})
	if err != nil {
		t.Fatalf("ListRequestLogs() error = %v, want nil", err)
	}
	if len(page.Items) != 1 || page.Items[0].StatusCode != 200 {
		t.Fatalf("ListRequestLogs() = %+v, want exactly one row with the original StatusCode 200 (duplicate must not overwrite)", page.Items)
	}
}

func TestListRequestLogsFiltersByTenantOutcomeAndTime(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)
	records := []model.RequestLog{
		{ID: "req_a1", TenantID: "tnt_a", Endpoint: "chat", StatusCode: 200, Outcome: "ok", CreatedAt: base},
		{ID: "req_a2", TenantID: "tnt_a", Endpoint: "chat", StatusCode: 429, Outcome: "rate_limited", CreatedAt: base.Add(time.Minute)},
		{ID: "req_b1", TenantID: "tnt_b", Endpoint: "embeddings", StatusCode: 200, Outcome: "ok", CreatedAt: base.Add(2 * time.Minute)},
	}
	if err := s.CreateRequestLogs(ctx, records); err != nil {
		t.Fatalf("CreateRequestLogs() error = %v, want nil", err)
	}

	tests := []struct {
		name    string
		filter  store.RequestLogFilter
		wantIDs []string
	}{
		{name: "tenant scope excludes other tenants", filter: store.RequestLogFilter{TenantID: "tnt_a"}, wantIDs: []string{"req_a2", "req_a1"}},
		{name: "empty tenant means every tenant (operator view)", filter: store.RequestLogFilter{}, wantIDs: []string{"req_b1", "req_a2", "req_a1"}},
		{name: "outcome filter", filter: store.RequestLogFilter{Outcome: "rate_limited"}, wantIDs: []string{"req_a2"}},
		{name: "request id exact match", filter: store.RequestLogFilter{RequestID: "req_b1"}, wantIDs: []string{"req_b1"}},
		{name: "since excludes earlier rows", filter: store.RequestLogFilter{Since: base.Add(90 * time.Second)}, wantIDs: []string{"req_b1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := s.ListRequestLogs(ctx, store.ListQuery{}, tt.filter)
			if err != nil {
				t.Fatalf("ListRequestLogs(%+v) error = %v, want nil", tt.filter, err)
			}
			gotIDs := make([]string, len(page.Items))
			for i, r := range page.Items {
				gotIDs[i] = r.ID
			}
			if len(gotIDs) != len(tt.wantIDs) {
				t.Fatalf("ListRequestLogs(%+v) = %v, want %v", tt.filter, gotIDs, tt.wantIDs)
			}
			for i := range gotIDs {
				if gotIDs[i] != tt.wantIDs[i] {
					t.Fatalf("ListRequestLogs(%+v)[%d] = %q, want %q (newest first)", tt.filter, i, gotIDs[i], tt.wantIDs[i])
				}
			}
		})
	}
}

func TestDeleteRequestLogsBeforeRemovesOnlyOlderRows(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	cutoff := time.Now()
	records := []model.RequestLog{
		{ID: "req_old", TenantID: "tnt_1", CreatedAt: cutoff.Add(-2 * time.Hour)},
		{ID: "req_new", TenantID: "tnt_1", CreatedAt: cutoff.Add(time.Hour)},
	}
	if err := s.CreateRequestLogs(ctx, records); err != nil {
		t.Fatalf("CreateRequestLogs() error = %v, want nil", err)
	}

	removed, err := s.DeleteRequestLogsBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteRequestLogsBefore() error = %v, want nil", err)
	}
	if removed != 1 {
		t.Fatalf("DeleteRequestLogsBefore() removed %d rows, want 1", removed)
	}
	page, err := s.ListRequestLogs(ctx, store.ListQuery{}, store.RequestLogFilter{TenantID: "tnt_1"})
	if err != nil {
		t.Fatalf("ListRequestLogs() error = %v, want nil", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "req_new" {
		t.Fatalf("ListRequestLogs() after cleanup = %+v, want only req_new to remain", page.Items)
	}
}
