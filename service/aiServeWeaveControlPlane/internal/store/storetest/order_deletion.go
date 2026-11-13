package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// OrderDeletion verifies persistent deletion from tenant history while keeping
// payment settlement, deduplication and internal history intact.
//
// OrderDeletion 验证租户历史的持久删除，同时保留付款结算、去重与内部历史。
func OrderDeletion(t *testing.T, st store.Billing) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	statuses := []string{model.OrderStatusPending, model.OrderStatusPaid, model.OrderStatusFailed, model.OrderStatusRefunded, model.OrderStatusDisputed}
	for i, status := range statuses {
		order := model.CheckoutOrder{ID: "ord_delete_" + status, TenantID: "tnt_delete", PackageID: "pkg", Currency: "USD", AmountMinor: 100,
			CreditsMicro: model.MicroPerCredit, Status: status, CreatedAt: now.Add(time.Duration(i) * time.Second), UpdatedAt: now}
		if err := st.CreateCheckoutOrder(ctx, &order); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteCheckoutOrder(ctx, "other", order.ID, now); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("cross-tenant delete", err)
		}
		if err := st.DeleteCheckoutOrder(ctx, order.TenantID, order.ID, now); err != nil {
			t.Fatal(status, err)
		}
		if err := st.DeleteCheckoutOrder(ctx, order.TenantID, order.ID, now); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("repeat delete", err)
		}
	}
	if err := st.DeleteCheckoutOrder(ctx, "tnt_delete", "missing", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("missing order", err)
	}
	visible := model.CheckoutOrder{ID: "ord_visible", TenantID: "tnt_delete", Status: model.OrderStatusFailed, CreatedAt: now.Add(-time.Hour), UpdatedAt: now}
	if err := st.CreateCheckoutOrder(ctx, &visible); err != nil {
		t.Fatal(err)
	}
	page, err := st.ListCheckoutOrders(ctx, store.ListQuery{Limit: 1}, store.CheckoutOrderFilter{TenantID: "tnt_delete", ExcludeTenantDeleted: true})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != visible.ID || page.NextCursor != "" {
		t.Fatal("deletion must filter before pagination", page, err)
	}
	// A late payment must still settle and its replay must remain a duplicate.
	event := store.BillingEvent{WebhookID: "wh_deleted", Kind: store.BillingEventPaymentSucceeded, OrderID: "ord_delete_pending", TenantID: "tnt_delete",
		PaymentID: "pay_deleted", AmountMinor: 100, Currency: "USD"}
	if _, err := st.ApplyBillingEvent(ctx, event, now); err != nil {
		t.Fatal(err)
	}
	if result, err := st.ApplyBillingEvent(ctx, event, now); err != nil || result.Outcome != store.BillingOutcomeDuplicate {
		t.Fatal("payment replay", result, err)
	}
	balance, err := st.GetTenantBilling(ctx, "tnt_delete")
	if err != nil || balance.BalanceMicro != model.MicroPerCredit {
		t.Fatal("late payment was lost or credited twice", balance, err)
	}
	page, err = st.ListCheckoutOrders(ctx, store.ListQuery{}, store.CheckoutOrderFilter{TenantID: "tnt_delete", ExcludeTenantDeleted: true})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != visible.ID {
		t.Fatal("payment resurrected deleted order", page, err)
	}
	page, err = st.ListCheckoutOrders(ctx, store.ListQuery{}, store.CheckoutOrderFilter{TenantID: "tnt_delete"})
	if err != nil || len(page.Items) != len(statuses)+1 {
		t.Fatal("internal history lost", page, err)
	}
	for _, row := range page.Items {
		if row.ID == event.OrderID && (row.Status != model.OrderStatusPaid || row.TenantDeletedAt == nil) {
			t.Fatal("deleted payment state", row)
		}
	}
}
