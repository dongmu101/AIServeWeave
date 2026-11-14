package memstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
)

func TestDeleteResponseTurnsBeforeMakesReapedTurnsUnknown(t *testing.T) {
	ctx := context.Background()
	s := memstore.New()
	cutoff := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for id, at := range map[string]time.Time{"resp_old": cutoff.Add(-time.Hour), "resp_new": cutoff.Add(time.Hour)} {
		if err := s.CreateResponseTurn(ctx, &model.ResponseTurn{ID: id, TenantID: "tnt_1", Messages: "[]", CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.DeleteResponseTurnsBefore(ctx, cutoff)
	if err != nil || got.RowsDeleted != 1 {
		t.Fatalf("DeleteResponseTurnsBefore = %+v, %v; want 1 row", got, err)
	}
	if _, err := s.GetResponseTurn(ctx, "tnt_1", "resp_old"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("reaped turn read = %v, want ErrNotFound", err)
	}
	if _, err := s.GetResponseTurn(ctx, "tnt_1", "resp_new"); err != nil {
		t.Errorf("kept turn read = %v", err)
	}
}

func TestDeleteAuditLogsBeforeRemovesOnlyOlderRecords(t *testing.T) {
	ctx := context.Background()
	s := memstore.New()
	cutoff := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for id, at := range map[string]time.Time{"aud_old": cutoff.Add(-time.Second), "aud_edge": cutoff, "aud_new": cutoff.Add(time.Hour)} {
		if err := s.AppendAudit(ctx, &model.AuditLog{ID: id, TenantID: "tnt_1", Action: "x", CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.DeleteAuditLogsBefore(ctx, cutoff)
	if err != nil || got.RowsDeleted != 1 {
		t.Fatalf("DeleteAuditLogsBefore = %+v, %v; want 1 row", got, err)
	}
	page, err := s.ListAudit(ctx, "tnt_1", store.ListQuery{}, store.AuditFilter{})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("remaining audit = %d, %v; want the record at the cutoff and the newer one", len(page.Items), err)
	}
}

func TestMaintainPartitionsIsANoOpInMemory(t *testing.T) {
	got, err := memstore.New().MaintainPartitions(context.Background(), time.Now())
	if err != nil || got.Skipped || got.Created != 0 {
		t.Errorf("MaintainPartitions = %+v, %v", got, err)
	}
}
