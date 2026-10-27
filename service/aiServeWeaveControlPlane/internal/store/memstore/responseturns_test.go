package memstore_test

import (
	"context"
	"errors"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
)

func newTestResponseTurn(id, tenantID string) *model.ResponseTurn {
	return &model.ResponseTurn{
		ID:       id,
		TenantID: tenantID,
		Model:    "test-model",
		Messages: `[{"Role":"user","Content":"hi"}]`,
	}
}

func TestCreateResponseTurnRejectsADuplicateIDAsConflict(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()

	if err := s.CreateResponseTurn(ctx, newTestResponseTurn("resp_1", "tenant-a")); err != nil {
		t.Fatalf("first CreateResponseTurn: %v", err)
	}
	err := s.CreateResponseTurn(ctx, newTestResponseTurn("resp_1", "tenant-a"))
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second CreateResponseTurn (duplicate id) = %v, want ErrConflict", err)
	}
}

func TestGetResponseTurnIsScopedToItsTenant(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	if err := s.CreateResponseTurn(ctx, newTestResponseTurn("resp_1", "tenant-a")); err != nil {
		t.Fatalf("CreateResponseTurn: %v", err)
	}

	if _, err := s.GetResponseTurn(ctx, "tenant-a", "resp_1"); err != nil {
		t.Errorf("GetResponseTurn(owning tenant) = %v, want nil", err)
	}
	if _, err := s.GetResponseTurn(ctx, "tenant-b", "resp_1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetResponseTurn(other tenant) = %v, want ErrNotFound — a turn belonging to another tenant must read exactly like one that does not exist", err)
	}
}

func TestGetResponseTurnUnknownIDIsNotFound(t *testing.T) {
	s := memstore.New()
	if _, err := s.GetResponseTurn(context.Background(), "tenant-a", "resp_unknown"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetResponseTurn(unknown id) = %v, want ErrNotFound", err)
	}
}
