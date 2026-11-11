package store

import (
	"AIServeWeave/common/accountaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"context"
	"errors"
	"strings"
	"time"
)

// AccountSales keeps the monthly account inventory and opens its fiat
// checkouts. A month is granted only by a verified payment, through
// ApplyBillingEvent. Targets cannot change after creation.
//
// AccountSales 保存包月账号库存并发起其法币结账。一个月的期限只由已验签的付款经
// ApplyBillingEvent 开通。目标创建后不能更改。
type AccountSales interface {
	ListAccountSales(context.Context) ([]model.AccountSale, error)
	SaveAccountSale(context.Context, model.AccountSale, bool) error
	// CreateAccountCheckoutOrder locks the account, re-checks it under
	// AccountCheckoutTerms and that no other tenant's checkout holds it, and
	// inserts the pending order. ErrNotFound for an archived account,
	// ErrConflict for a changed price, a held or occupied account, or the
	// renewal cap.
	//
	// CreateAccountCheckoutOrder 锁定账号，按 AccountCheckoutTerms 复核并确认没有其他
	// 商户的结账占用它，然后插入待付款订单。账号已下架返回 ErrNotFound；价格已变、被占用
	// 或续费上限冲突返回 ErrConflict。
	CreateAccountCheckoutOrder(ctx context.Context, order *model.CheckoutOrder, now time.Time) error
	// FailCheckoutOrder marks a pending order whose checkout never opened as
	// failed, so it stops holding its account.
	//
	// FailCheckoutOrder 把结账未能发起的待付款订单标记为失败，使其不再占用账号。
	FailCheckoutOrder(ctx context.Context, orderID string, now time.Time) error
}

// AccountCheckoutTerms decides whether tenantID may open a checkout for one
// month of sale at amountMinor in currency.
//
// AccountCheckoutTerms 判断 tenantID 能否以 currency 计价的 amountMinor 为 sale 发起
// 一个月的结账。
func AccountCheckoutTerms(sale model.AccountSale, tenantID string, amountMinor int64, currency string, now time.Time) error {
	if !sale.Active || sale.PriceMinor <= 0 || sale.Currency == "" || sale.DodoProductID == "" {
		return ErrNotFound
	}
	if sale.PriceMinor != amountMinor || !strings.EqualFold(sale.Currency, currency) {
		return ErrConflict
	}
	_, err := AccountGrantTerms(sale, tenantID, now)
	return err
}

// AccountGrantTerms is the expiry one more month of sale gives tenantID from
// now: from the tenant's own unexpired expiry, else from now. ErrConflict
// when another tenant holds it unexpired or the expiry would pass the
// ten-year renewal cap. Listing state is not checked: a paid month is
// granted even if the account was archived after its checkout opened.
//
// AccountGrantTerms 是从 now 起再给 tenantID 一个月后的到期时间：本商户尚未到期时从
// 原到期时间延长，否则从 now 起算。其他商户持有且未到期，或超过十年续费上限时返回
// ErrConflict。不检查上架状态：结账发起后才下架的账号，已付款的一个月照常开通。
func AccountGrantTerms(sale model.AccountSale, tenantID string, now time.Time) (time.Time, error) {
	if sale.TenantID != "" && sale.TenantID != tenantID && now.Before(sale.ExpiresAt) {
		return time.Time{}, ErrConflict
	}
	start := now
	if sale.TenantID == tenantID && sale.ExpiresAt.After(start) {
		start = sale.ExpiresAt
	}
	end := accountaccess.NextMonth(start)
	if end.After(now.AddDate(10, 0, 0)) {
		return time.Time{}, ErrConflict
	}
	return end, nil
}

// AccountCheckoutHeld reports whether order, another tenant's checkout,
// still holds its account at now.
//
// AccountCheckoutHeld 报告其他商户的 order 结账在 now 时是否仍占用其账号。
func AccountCheckoutHeld(order model.CheckoutOrder, tenantID string, now time.Time) bool {
	return order.TenantID != tenantID && order.Status == model.OrderStatusPending &&
		now.Before(order.CreatedAt.Add(model.AccountCheckoutHold))
}

// grantAccount gives a paid account order its month. A paid order that
// cannot be granted keeps its payment and a nil expiry, and the outcome is a
// mismatch an operator settles with a refund.
//
// grantAccount 为已付款的账号订单开通一个月。无法开通的已付款订单保留付款且到期时间
// 为 nil，结果为需要运维以退款处理的 mismatch。
func grantAccount(tx BillingTx, result BillingEventResult, order model.CheckoutOrder, now time.Time) (BillingEventResult, error) {
	// Lock order shared with checkout: order -> tenant billing -> account.
	// 与结账一致的加锁顺序：订单 -> 商户计费行 -> 账号。
	if _, err := tx.TenantBilling(order.TenantID, now); err != nil {
		return BillingEventResult{}, err
	}
	sale, err := tx.AccountSaleByID(order.AccountID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return BillingEventResult{}, err
	}
	var end time.Time
	if err == nil {
		end, err = AccountGrantTerms(sale, order.TenantID, now)
	}
	if err != nil {
		result.Outcome = BillingOutcomeMismatch
		return result, nil
	}
	if err := tx.GrantAccount(order.AccountID, order.TenantID, end, now); err != nil {
		return BillingEventResult{}, err
	}
	order.AccountExpiresAt = &end
	if err := tx.SaveOrder(order); err != nil {
		return BillingEventResult{}, err
	}
	result.Outcome, result.AccessChanged = BillingOutcomeApplied, true
	return result, nil
}
