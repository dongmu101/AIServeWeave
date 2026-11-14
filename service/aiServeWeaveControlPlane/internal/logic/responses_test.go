package logic_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
)

func mustCreateResponseTurn(t *testing.T, f *fixture, responseID, previousResponseID string) {
	t.Helper()
	if _, err := f.svc.CreateResponseTurn(context.Background(), logic.CreateResponseTurnParams{
		ResponseID:         responseID,
		TenantID:           f.tenant.ID,
		PreviousResponseID: previousResponseID,
		Model:              "test-model",
		Messages:           `[{"Role":"user","Content":"hi"}]`,
	}); err != nil {
		t.Fatalf("CreateResponseTurn: %v", err)
	}
}

func TestCreateResponseTurnIsIdempotentOnADuplicateID(t *testing.T) {
	f := newFixture(t)
	mustCreateResponseTurn(t, f, "resp_1", "")

	// A retried create must come back as a successful read of the existing
	// row, not a conflict — the same idempotent-retry shape CreateJob gives.
	//
	// 一次重试的创建必须以对既有行的一次成功读取收场，而不是冲突——与
	// CreateJob 给出的是同一种幂等重试形状。
	second, err := f.svc.CreateResponseTurn(context.Background(), logic.CreateResponseTurnParams{
		ResponseID: "resp_1", TenantID: f.tenant.ID, Model: "test-model", Messages: `[{"Role":"user","Content":"hi"}]`,
	})
	if err != nil {
		t.Fatalf("CreateResponseTurn (duplicate) = %v, want a nil error (idempotent)", err)
	}
	if second.ID != "resp_1" {
		t.Errorf("CreateResponseTurn (duplicate) = %+v, want the existing row", second)
	}
}

func TestCreateResponseTurnDuplicateIDInAnotherTenantIsAConflict(t *testing.T) {
	f := newFixture(t)
	mustCreateResponseTurn(t, f, "resp_1", "")

	otherTenant, _, err := f.svc.CreateTenant(context.Background(), "Other", "owner2@example.com", testPassword, "10.0.0.1")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	_, err = f.svc.CreateResponseTurn(context.Background(), logic.CreateResponseTurnParams{
		ResponseID: "resp_1", TenantID: otherTenant.ID, Messages: `[{"Role":"user","Content":"hi"}]`,
	})
	if !errors.Is(err, logic.ErrConflict) {
		t.Fatalf("CreateResponseTurn(same id, other tenant) = %v, want ErrConflict", err)
	}
}

func TestGetResponseTurnIsScopedToItsTenant(t *testing.T) {
	f := newFixture(t)
	mustCreateResponseTurn(t, f, "resp_1", "")

	if _, err := f.svc.GetResponseTurn(context.Background(), f.tenant.ID, "resp_1"); err != nil {
		t.Errorf("GetResponseTurn(owning tenant) = %v, want nil", err)
	}
	if _, err := f.svc.GetResponseTurn(context.Background(), "tenant-other", "resp_1"); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("GetResponseTurn(other tenant) = %v, want ErrNotFound", err)
	}
}

func TestGetResponseTurnUnknownIDIsNotFound(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.GetResponseTurn(context.Background(), f.tenant.ID, "resp_unknown"); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("GetResponseTurn(unknown) = %v, want ErrNotFound", err)
	}
}

func TestCreateResponseTurnRejectsEmptyFields(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name string
		p    logic.CreateResponseTurnParams
	}{
		{"empty response id", logic.CreateResponseTurnParams{TenantID: f.tenant.ID, Messages: "[]"}},
		{"empty tenant id", logic.CreateResponseTurnParams{ResponseID: "resp_1", Messages: "[]"}},
		{"empty messages", logic.CreateResponseTurnParams{ResponseID: "resp_1", TenantID: f.tenant.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := f.svc.CreateResponseTurn(context.Background(), tt.p); !errors.Is(err, logic.ErrInvalidInput) {
				t.Errorf("CreateResponseTurn(%+v) = %v, want ErrInvalidInput", tt.p, err)
			}
		})
	}
}

// TestReapedResponseTurnReadsLikeAnUnknownID pins the retention contract: once
// the sweeper removes a turn, a previous_response_id naming it gets exactly the
// answer an id that never existed gets.
//
// TestReapedResponseTurnReadsLikeAnUnknownID 固定保留期契约：清理协程移除一轮之后，
// 指向它的 previous_response_id 得到的答复与从未存在的 id 完全相同。
func TestReapedResponseTurnReadsLikeAnUnknownID(t *testing.T) {
	f := newFixture(t)
	mustCreateResponseTurn(t, f, "resp_reaped", "")
	if _, err := f.store.DeleteResponseTurnsBefore(context.Background(), f.clock.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, reaped := f.svc.GetResponseTurn(context.Background(), f.tenant.ID, "resp_reaped")
	_, unknown := f.svc.GetResponseTurn(context.Background(), f.tenant.ID, "resp_never")
	if !errors.Is(reaped, logic.ErrNotFound) || reaped.Error() != unknown.Error() {
		t.Errorf("reaped = %v, unknown = %v; want the same not-found answer", reaped, unknown)
	}
}
