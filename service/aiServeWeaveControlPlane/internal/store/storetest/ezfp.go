package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// EzfpStore is what the 易支付 scenario uses.
type EzfpStore interface {
	store.Billing
	store.AccountSales
	store.AccountPresales
}

// EzfpOrders checks 易支付 orders: the provider and payment type persist and
// filter, a payment settles only its own provider's order and only the
// trade the checkout opened, its replay is a duplicate, a refund under the
// prefixed ids links to the request that held its credits, an order from
// before the provider column still behaves as Dodo's, and CNY items without
// a Dodo product can be checked out through 易支付 alone.
//
// EzfpOrders 检查易支付订单：服务商与付款方式能持久化并用于筛选；付款只结算其自身服务商的
// 订单、且只结算结账开出的那笔交易；重放视为重复；带前缀 id 的退款关联到冻结其点数的申请；
// 服务商列出现之前的订单仍按 Dodo 处理；没有 Dodo 产品的 CNY 商品只能经易支付结账。
func EzfpOrders(t *testing.T, st EzfpStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	const tenant = "tnt_ezfp"
	apply := func(event store.BillingEvent) store.BillingEventResult {
		t.Helper()
		result, err := st.ApplyBillingEvent(ctx, event, now)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	payment := func(webhook, order, trade, provider string, amount int64) store.BillingEvent {
		return store.BillingEvent{
			WebhookID: webhook, Type: "ezfp.payment", Kind: store.BillingEventPaymentSucceeded, Provider: provider,
			OrderID: order, TenantID: tenant, PaymentID: trade, CheckoutSessionID: trade, AmountMinor: amount, Currency: "CNY",
		}
	}

	order := model.CheckoutOrder{
		ID: "ord_ezfp_1", TenantID: tenant, PackageID: "pkg_cny", AmountMinor: 6800, Currency: "CNY",
		CreditsMicro: 10 * model.MicroPerCredit, Provider: model.PaymentProviderEzfp, PayType: model.EzfpPayTypeWxpay,
		Status: model.OrderStatusPending, CreatedBy: "owner", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateCheckoutOrder(ctx, &order); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCheckoutOrderSession(ctx, order.ID, "ezfp:T1", now); err != nil {
		t.Fatal(err)
	}
	legacy := model.CheckoutOrder{
		ID: "ord_legacy", TenantID: tenant, PackageID: "pkg_usd", DodoProductID: "pdt", AmountMinor: 500, Currency: "USD",
		CreditsMicro: model.MicroPerCredit, Status: model.OrderStatusPending, CreatedBy: "owner", CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
	}
	if err := st.CreateCheckoutOrder(ctx, &legacy); err != nil {
		t.Fatal(err)
	}

	// Provider and creation window filter; the window is strict.
	page, err := st.ListCheckoutOrders(ctx, store.ListQuery{}, store.CheckoutOrderFilter{
		Provider: model.PaymentProviderEzfp, Status: model.OrderStatusPending, CreatedAfter: now.Add(-time.Hour), CreatedBefore: now.Add(time.Second),
	})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != order.ID || page.Items[0].PayType != model.EzfpPayTypeWxpay ||
		page.Items[0].PaymentProvider() != model.PaymentProviderEzfp {
		t.Fatalf("filtered orders = %+v, %v", page.Items, err)
	}
	if page, err := st.ListCheckoutOrders(ctx, store.ListQuery{}, store.CheckoutOrderFilter{Provider: model.PaymentProviderEzfp, CreatedBefore: now}); err != nil || len(page.Items) != 0 {
		t.Fatalf("an order at the window's edge was listed: %+v, %v", page.Items, err)
	}
	if got, err := st.GetCheckoutOrder(ctx, legacy.ID); err != nil || got.Provider != "" || got.PaymentProvider() != model.PaymentProviderDodo {
		t.Fatalf("legacy order = %+v, %v", got, err)
	}

	// A Dodo event cannot settle an 易支付 order, nor a trade the checkout
	// did not open.
	if r := apply(payment("wh_dodo", order.ID, "pay_x", "", 6800)); r.Outcome != store.BillingOutcomeMismatch {
		t.Fatalf("a Dodo event on an 易支付 order: %s", r.Outcome)
	}
	if r := apply(payment("ezfp:pay:T2", order.ID, "ezfp:T2", model.PaymentProviderEzfp, 6800)); r.Outcome != store.BillingOutcomeMismatch {
		t.Fatalf("another trade: %s", r.Outcome)
	}
	if r := apply(payment("ezfp:pay:T1", order.ID, "ezfp:T1", model.PaymentProviderEzfp, 6800)); r.Outcome != store.BillingOutcomeApplied {
		t.Fatalf("the trade: %s", r.Outcome)
	}
	if r := apply(payment("ezfp:pay:T1", order.ID, "ezfp:T1", model.PaymentProviderEzfp, 6800)); r.Outcome != store.BillingOutcomeDuplicate {
		t.Fatalf("the replay: %s", r.Outcome)
	}
	paid, err := st.GetCheckoutOrder(ctx, order.ID)
	if err != nil || paid.Status != model.OrderStatusPaid || paid.DodoPaymentID == nil || *paid.DodoPaymentID != "ezfp:T1" ||
		paid.PaidAmountMinor != 6800 || paid.PaidCurrency != "CNY" || paid.Provider != model.PaymentProviderEzfp {
		t.Fatalf("paid order = %+v, %v", paid, err)
	}
	if row, err := st.GetTenantBilling(ctx, tenant); err != nil || row.BalanceMicro != 10*model.MicroPerCredit {
		t.Fatalf("balance = %+v, %v", row, err)
	}
	// An order from before the provider column takes a Dodo event as before.
	if r := apply(store.BillingEvent{WebhookID: "wh_legacy", Kind: store.BillingEventPaymentSucceeded, OrderID: legacy.ID, TenantID: tenant,
		PaymentID: "pay_legacy", AmountMinor: 500, Currency: "USD"}); r.Outcome != store.BillingOutcomeApplied {
		t.Fatalf("legacy payment: %s", r.Outcome)
	}

	// A refund made under prefixed ids settles the request that held its
	// credits, and a replay changes nothing.
	req, _, _, err := st.OpenRefundRequest(ctx, model.RefundRequest{
		ID: "rfd_ezfp_1", OrderID: order.ID, Source: model.RefundSourceOperator, Status: model.RefundStatusProcessing,
		Reason: "test", RequestedBy: "operator", ReviewedBy: "operator",
	}, store.RefundPolicy{}, now)
	if err != nil || req.AmountMinor != 6800 {
		t.Fatalf("refund request = %+v, %v", req, err)
	}
	if err := st.RecordRefundSubmitted(ctx, req.ID, "ezfp:R1", now); err != nil {
		t.Fatal(err)
	}
	refund := store.BillingEvent{
		WebhookID: "ezfp:refund:R1", Type: "ezfp.refund", Kind: store.BillingEventRefundSucceeded, Provider: model.PaymentProviderEzfp,
		OrderID: order.ID, TenantID: tenant, PaymentID: "ezfp:T1", RefundID: "ezfp:R1", RefundRequestID: req.ID,
		AmountMinor: 6800, Currency: "CNY",
	}
	if r := apply(refund); r.Outcome != store.BillingOutcomeApplied {
		t.Fatalf("refund: %s", r.Outcome)
	}
	if r := apply(refund); r.Outcome != store.BillingOutcomeDuplicate {
		t.Fatalf("refund replay: %s", r.Outcome)
	}
	if got, err := st.GetRefundRequest(ctx, req.ID); err != nil || got.Status != model.RefundStatusSucceeded {
		t.Fatalf("refund request after = %+v, %v", got, err)
	}
	if got, _ := st.GetCheckoutOrder(ctx, order.ID); got.Status != model.OrderStatusRefunded || got.RefundedMinor != 6800 {
		t.Fatalf("refunded order = %+v", got)
	}
	if row, _ := st.GetTenantBilling(ctx, tenant); row.BalanceMicro != model.MicroPerCredit {
		t.Fatalf("balance after refund = %d", row.BalanceMicro)
	}

	// CNY items without a Dodo product: 易支付 only.
	sale := model.AccountSale{ID: "acc_cny", NodeID: "agent-cny", RuntimeID: "claude-cny", Kind: model.AccountKindClaude, Name: "CNY",
		PriceMinor: 9900, Currency: "CNY", Active: true, ExpiresAt: time.Unix(0, 0).UTC(), UpdatedAt: now}
	if err := st.SaveAccountSale(ctx, sale, true); err != nil {
		t.Fatal(err)
	}
	accountOrder := func(id, provider string) error {
		o := model.CheckoutOrder{ID: id, TenantID: tenant, AccountID: sale.ID, AmountMinor: 9900, Currency: "CNY", Provider: provider,
			Status: model.OrderStatusPending, CreatedBy: "owner", CreatedAt: now, UpdatedAt: now}
		return st.CreateAccountCheckoutOrder(ctx, &o, now)
	}
	if err := accountOrder("ord_acc_dodo", model.PaymentProviderDodo); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a Dodo checkout of a product-less account: %v", err)
	}
	if err := accountOrder("ord_acc_ezfp", model.PaymentProviderEzfp); err != nil {
		t.Fatal(err)
	}
	presale := model.AccountPresale{ID: "pre_cny", Kind: model.AccountKindCodex, Name: "CNY", PriceMinor: 1500, Currency: "CNY",
		Capacity: 1, Active: true, UpdatedAt: now}
	if err := st.SaveAccountPresale(ctx, presale, true); err != nil {
		t.Fatal(err)
	}
	presaleOrder := func(id, provider string) error {
		o := model.CheckoutOrder{ID: id, TenantID: tenant, PresaleID: presale.ID, AmountMinor: 1500, Currency: "CNY", Provider: provider,
			Status: model.OrderStatusPending, CreatedBy: "owner", CreatedAt: now, UpdatedAt: now}
		return st.CreatePresaleCheckoutOrder(ctx, &o, now)
	}
	if err := presaleOrder("ord_pre_dodo", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a Dodo checkout of a product-less presale: %v", err)
	}
	if err := presaleOrder("ord_pre_ezfp", model.PaymentProviderEzfp); err != nil {
		t.Fatal(err)
	}
	if err := presaleOrder("ord_pre_full", model.PaymentProviderEzfp); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a full presale: %v", err)
	}
}
