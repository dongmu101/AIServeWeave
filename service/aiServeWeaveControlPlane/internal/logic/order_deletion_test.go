package logic_test

import (
	"context"
	"errors"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

func TestDeleteBillingOrderPermissionsAndTenantHistory(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	order := model.CheckoutOrder{ID: "ord_delete", TenantID: f.tenant.ID, Status: model.OrderStatusPaid, CreatedAt: f.clock.Now()}
	if err := f.store.CreateCheckoutOrder(ctx, &order); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeleteBillingOrder(ctx, f.ownerAt, order.ID); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("disabled billing", err)
	}
	f.svc = logic.New(f.store, f.clock, logic.WithAccountSales())
	for _, actor := range []logic.Actor{f.actorWithRole(model.RoleMember), accountOperator} {
		if err := f.svc.DeleteBillingOrder(ctx, actor, order.ID); !errors.Is(err, logic.ErrForbidden) {
			t.Fatal("unauthorized actor", actor.Role, err)
		}
	}
	foreign := f.ownerAt
	foreign.TenantID = "another_tenant"
	if err := f.svc.DeleteBillingOrder(ctx, foreign, order.ID); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("cross-tenant deletion", err)
	}
	if err := f.svc.DeleteBillingOrder(ctx, f.actorWithRole(model.RoleAdmin), order.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeleteBillingOrder(ctx, f.ownerAt, order.ID); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("repeat deletion", err)
	}
	page, err := f.svc.ListBillingOrders(ctx, f.ownerAt, store.ListQuery{}, store.CheckoutOrderFilter{TenantID: foreign.TenantID})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("tenant still sees deleted order", page, err)
	}
	page, err = f.svc.ListBillingOrders(ctx, accountOperator, store.ListQuery{}, store.CheckoutOrderFilter{TenantID: f.tenant.ID})
	if err != nil || len(page.Items) != 1 || page.Items[0].Status != model.OrderStatusPaid {
		t.Fatal("operator history lost", page, err)
	}
	audit, err := f.store.ListAudit(ctx, f.tenant.ID, store.ListQuery{}, store.AuditFilter{Action: model.ActionBillingOrderDelete})
	if err != nil || len(audit.Items) != 1 || audit.Items[0].Target != order.ID {
		t.Fatal("missing deletion audit", audit, err)
	}
}
