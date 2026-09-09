package logic_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"AIServeWeave/common/modelroute"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
)

func TestRoutesConcurrentCASAndImmutableRollback(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	s := logic.New(st, nil)
	actor := logic.Actor{UserID: "operator", TenantID: model.PlatformScope, Role: model.RolePlatformOperator}
	routes := []modelroute.Route{{Model: "alias", Targets: []modelroute.Target{{RuntimeModel: "backend", NodeSelector: map[string]string{"region": "old"}}}}}
	var wg sync.WaitGroup
	var winners atomic.Int32
	for range 20 {
		wg.Go(func() {
			_, err := s.PublishRoutes(ctx, actor, 0, routes)
			if err == nil {
				winners.Add(1)
			} else if !errors.Is(err, logic.ErrConflict) {
				t.Errorf("publish error=%v want conflict", err)
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("winners=%d want1", winners.Load())
	}
	routes[0].Targets[0].NodeSelector["region"] = "mutated"
	original, err := s.RouteRevision(ctx, 1)
	if err != nil || original.Routes[0].Targets[0].NodeSelector["region"] != "old" {
		t.Fatalf("snapshot=%+v err=%v want immutable old", original, err)
	}
	original.Routes[0].Targets[0].RuntimeModel = "mutated"
	got, err := s.RollbackRoutes(ctx, actor, 1, 1)
	if err != nil || got.Revision != 2 || got.RollbackOf != 1 || got.Routes[0].Targets[0].RuntimeModel != "backend" {
		t.Fatalf("rollback=%+v err=%v want immutable revision2", got, err)
	}
	audit, err := st.ListAudit(ctx, model.PlatformScope, store.ListQuery{}, store.AuditFilter{})
	if err != nil || len(audit.Items) != 2 {
		t.Fatalf("audit=%+v err=%v want2", audit, err)
	}
	if _, err := s.PublishRoutes(ctx, logic.Actor{TenantID: "tenant", Role: model.RoleOwner}, 2, routes); !errors.Is(err, logic.ErrForbidden) {
		t.Fatalf("tenant error=%v want forbidden", err)
	}
	if _, err := s.PublishRoutes(ctx, actor, 2, []modelroute.Route{{Model: "invalid"}}); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatalf("invalid error=%v want invalid input", err)
	}
}

func TestRoutesRejectCorruptStoredSnapshotsBeforeRollback(t *testing.T) {
	for _, tc := range []struct{ name, body, digest string }{{"null", "null", "bad"}, {"invalid target", `[{"model":"alias","targets":[]}]`, "bad"}, {"digest mismatch", "[]", "bad"}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := memstore.New()
			row := model.RouteRevision{RoutesJSON: tc.body, Digest: tc.digest}
			audit := model.AuditLog{}
			if err := st.PublishRouteRevision(ctx, 0, &row, &audit); err != nil {
				t.Fatal(err)
			}
			s := logic.New(st, nil)
			if _, err := s.CurrentRoutes(ctx); err == nil {
				t.Fatal("corrupt current read succeeded; want error")
			}
			actor := logic.Actor{UserID: "operator", TenantID: model.PlatformScope, Role: model.RolePlatformOperator}
			if _, err := s.RollbackRoutes(ctx, actor, 1, 1); err == nil {
				t.Fatal("corrupt rollback succeeded; want error")
			}
			current, err := st.CurrentRouteRevision(ctx)
			if err != nil || current.Revision != 1 {
				t.Fatalf("current revision=%d err=%v want unchanged1", current.Revision, err)
			}
		})
	}
}

func TestRoutesHistoryCapacityPreservesCurrent(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	s := logic.New(st, nil)
	actor := logic.Actor{UserID: "operator", TenantID: model.PlatformScope, Role: model.RolePlatformOperator}
	for i := int64(0); i < modelroute.MaxRevisions; i++ {
		if _, err := s.PublishRoutes(ctx, actor, i, []modelroute.Route{}); err != nil {
			t.Fatalf("publish%d err=%v want nil", i, err)
		}
	}
	if _, err := s.PublishRoutes(ctx, actor, modelroute.MaxRevisions, []modelroute.Route{}); !errors.Is(err, logic.ErrRouteCapacity) {
		t.Fatalf("full history err=%v want capacity", err)
	}
	current, err := s.CurrentRoutes(ctx)
	if err != nil || current.Revision != modelroute.MaxRevisions {
		t.Fatalf("current=%+v err=%v want capacity revision", current, err)
	}
	page, err := s.RoutesHistory(ctx, 0, 2)
	if err != nil || len(page.Items) != 2 || page.Items[0].Revision != modelroute.MaxRevisions || page.NextBefore != modelroute.MaxRevisions-1 {
		t.Fatalf("history=%+v err=%v want two newest with cursor", page, err)
	}
}
