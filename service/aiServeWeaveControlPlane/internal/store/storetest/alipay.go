package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// AlipayRedemptions checks the Alipay purchase intents and redemptions: one
// pending redemption per tenant, the same action id resuming rather than
// creating, credits booked exactly once on success, concurrent settlement
// and creation included.
//
// AlipayRedemptions 检查支付宝购买意图与兑换：每租户只有一个待确认兑换，同一 action id
// 恢复而不是新建，成功时点数恰好入账一次，包括并发结算与并发创建。
func AlipayRedemptions(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	tenant := model.NewID(model.PrefixTenant)

	purchase := model.AlipayPurchase{
		ID: model.NewID(model.PrefixAlipayPurchase), TenantID: tenant, ActionID: "action-purchase-1", SkuID: "sku_a",
		CallbackURL: "https://console.example/billing", Status: model.AlipayPurchaseUnknown, CreatedBy: "usr_x", CreatedAt: now, UpdatedAt: now,
	}
	got, err := st.CreateAlipayPurchase(ctx, purchase)
	if err != nil || got.ID != purchase.ID {
		t.Fatal("create purchase", got, err)
	}
	again := purchase
	again.ID = model.NewID(model.PrefixAlipayPurchase)
	if got, err := st.CreateAlipayPurchase(ctx, again); err != nil || got.ID != purchase.ID {
		t.Fatal("same action must return the saved purchase", got.ID, err)
	}
	again.SkuID = "sku_b"
	if _, err := st.CreateAlipayPurchase(ctx, again); !errors.Is(err, store.ErrConflict) {
		t.Fatal("changed sku under the same action", err)
	}
	purchase.Status, purchase.PurchaseURL = model.AlipayPurchaseReady, "https://pay.example/entry"
	if err := st.SaveAlipayPurchase(ctx, purchase); err != nil {
		t.Fatal(err)
	}
	if got, err := st.CreateAlipayPurchase(ctx, again2(purchase)); err != nil || got.Status != model.AlipayPurchaseReady || got.PurchaseURL != purchase.PurchaseURL {
		t.Fatal("saved purchase result", got, err)
	}

	first := redemption(tenant, "action-redeem-1", 5, now)
	saved, err := st.CreateAlipayRedemption(ctx, first)
	if err != nil || saved.ID != first.ID || saved.Status != model.AlipayRedemptionPending {
		t.Fatal("create redemption", saved, err)
	}
	retry := redemption(tenant, "action-redeem-1", 5, now)
	if got, err := st.CreateAlipayRedemption(ctx, retry); err != nil || got.ID != first.ID {
		t.Fatal("same action must resume", got.ID, err)
	}
	if _, err := st.CreateAlipayRedemption(ctx, redemption(tenant, "action-redeem-1", 6, now)); !errors.Is(err, store.ErrConflict) {
		t.Fatal("changed amount under the same action", err)
	}
	if _, err := st.CreateAlipayRedemption(ctx, redemption(tenant, "action-redeem-2", 1, now)); !errors.Is(err, store.ErrConflict) {
		t.Fatal("second redemption while one is pending", err)
	}
	if page, err := st.ListAlipayRedemptions(ctx, store.ListQuery{Limit: 10}, store.AlipayRedemptionFilter{Status: model.AlipayRedemptionPending, CreatedBefore: now}); err != nil || len(page.Items) != 0 {
		t.Fatal("CreatedBefore must exclude a redemption created at the bound", page.Items, err)
	}

	// Settled concurrently, as the tenant's call and the recovery might.
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := st.SettleAlipayRedemption(ctx, store.AlipayOutcome{ID: first.ID, Succeeded: true, ConsumeOrderID: "co_1"}, now.Add(time.Second)); err != nil {
				t.Error("settle", err)
			}
		}()
	}
	wg.Wait()
	row, err := st.GetTenantBilling(ctx, tenant)
	if err != nil || row.BalanceMicro != first.CreditsMicro {
		t.Fatal("credits must be booked once", row.BalanceMicro, err)
	}
	settled, err := st.GetAlipayRedemption(ctx, first.ID)
	if err != nil || settled.Status != model.AlipayRedemptionSucceeded || settled.ConsumeOrderID != "co_1" {
		t.Fatal("settled redemption", settled, err)
	}
	if got, _, err := st.SettleAlipayRedemption(ctx, store.AlipayOutcome{ID: first.ID}, now.Add(2*time.Second)); err != nil || got.Status != model.AlipayRedemptionSucceeded {
		t.Fatal("a settled redemption must not change", got, err)
	}
	ledger, err := st.ListCreditLedger(ctx, store.ListQuery{Limit: 10}, store.CreditLedgerFilter{TenantID: tenant, Kind: model.LedgerKindAlipayTopup})
	if err != nil || len(ledger.Items) != 1 || ledger.Items[0].Ref != first.ID || ledger.Items[0].AmountMicro != first.CreditsMicro {
		t.Fatal("ledger", ledger.Items, err)
	}

	// With nothing pending, concurrent new clicks still leave one pending.
	var created sync.Map
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := redemption(tenant, "action-race-"+string(rune('a'+i))+"-xxxx", 1, now.Add(time.Minute))
			if got, err := st.CreateAlipayRedemption(ctx, r); err == nil {
				created.Store(got.ID, true)
			} else if !errors.Is(err, store.ErrConflict) {
				t.Error("racing create", err)
			}
		}()
	}
	wg.Wait()
	count := 0
	var pendingID string
	created.Range(func(k, _ any) bool { count++; pendingID = k.(string); return true })
	if count != 1 {
		t.Fatal("racing creates must leave exactly one pending", count)
	}
	failed, changed, err := st.SettleAlipayRedemption(ctx, store.AlipayOutcome{ID: pendingID, ReasonCode: "NOW_PAY_QUOTA_INSUFFICIENT"}, now.Add(2*time.Minute))
	if err != nil || changed || failed.Status != model.AlipayRedemptionFailed || failed.ReasonCode != "NOW_PAY_QUOTA_INSUFFICIENT" {
		t.Fatal("failed settlement", failed, changed, err)
	}
	if row, _ := st.GetTenantBilling(ctx, tenant); row.BalanceMicro != first.CreditsMicro {
		t.Fatal("a failure must not book credits", row.BalanceMicro)
	}
	page, err := st.ListAlipayRedemptions(ctx, store.ListQuery{Limit: 10}, store.AlipayRedemptionFilter{TenantID: tenant})
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != pendingID {
		t.Fatal("list newest first", page.Items, err)
	}
	if _, _, err := st.SettleAlipayRedemption(ctx, store.AlipayOutcome{ID: "apr_missing"}, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("missing redemption", err)
	}
}

func again2(p model.AlipayPurchase) model.AlipayPurchase {
	p.ID = model.NewID(model.PrefixAlipayPurchase)
	return p
}

func redemption(tenant, action string, amount int64, now time.Time) model.AlipayRedemption {
	return model.AlipayRedemption{
		ID: model.NewID(model.PrefixAlipayRedemption), TenantID: tenant, ActionID: action, Amount: amount,
		QuotaUnit: "POINT", CreditsMicro: amount * 2_000_000, Reason: "test", CreatedBy: "usr_x", CreatedAt: now, UpdatedAt: now,
	}
}
