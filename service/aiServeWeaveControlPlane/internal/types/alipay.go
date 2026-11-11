package types

import "time"

// AlipayPlan is one plan configured in Alipay, priced as Alipay reports it.
//
// AlipayPlan 是支付宝中配置的一个套餐，价格以支付宝报告为准。
type AlipayPlan struct {
	SkuID       string `json:"sku_id"`
	Name        string `json:"name"`
	Price       string `json:"price"`
	Currency    string `json:"currency"`
	QuotaUnit   string `json:"quota_unit"`
	QuotaAmount int64  `json:"quota_amount"`
}

// AlipayAccess is the tenant's quota in Alipay.
//
// AlipayAccess 是租户在支付宝中的额度。
type AlipayAccess struct {
	Total     int64 `json:"total"`
	Used      int64 `json:"used"`
	Remaining int64 `json:"remaining"`
}

// AlipayOverviewResponse is GET /admin/v1/billing/alipay. Access is null
// when the quota could not be read. Supported is false for a merchant
// configuration credits cannot be redeemed from (a one-time or duration
// package); CanPurchase and CanRedeem say what 一人付 allows right now.
//
// AlipayOverviewResponse 是 GET /admin/v1/billing/alipay 的响应。无法读取额度时
// Access 为 null。商户配置无法兑换点数（买断或时长包）时 Supported 为 false；CanPurchase
// 与 CanRedeem 说明一人付此刻允许的操作。
type AlipayOverviewResponse struct {
	CapabilityStatus    string            `json:"capability_status"`
	ProductName         string            `json:"product_name"`
	BillingMode         string            `json:"billing_mode"`
	QuotaUnit           string            `json:"quota_unit"`
	Supported           bool              `json:"supported"`
	CanPurchase         bool              `json:"can_purchase"`
	CanRedeem           bool              `json:"can_redeem"`
	Plans               []AlipayPlan      `json:"plans"`
	Access              *AlipayAccess     `json:"access"`
	CreditsPerUnitMicro int64             `json:"credits_per_unit_micro"`
	MaxAmount           int64             `json:"max_amount"`
	Pending             *AlipayRedemption `json:"pending"`
}

// AlipayPurchaseRequest is POST /admin/v1/billing/alipay/purchases.
// ActionID identifies one click and is reused when that click is retried;
// an empty SkuID lets the buyer choose in Alipay.
//
// AlipayPurchaseRequest 是 POST /admin/v1/billing/alipay/purchases 的请求。ActionID
// 标识一次点击，重试该点击时沿用；SkuID 为空时由购买者在支付宝选择。
type AlipayPurchaseRequest struct {
	ActionID string `json:"action_id"`
	SkuID    string `json:"sku_id,omitempty"`
}

// AlipayPurchaseResponse is an opened purchase entry. It is not a payment.
//
// AlipayPurchaseResponse 是一个已创建的购买入口，不代表付款。
type AlipayPurchaseResponse struct {
	ID          string `json:"id"`
	PurchaseURL string `json:"purchase_url"`
	ExpireTime  string `json:"expire_time"`
}

// AlipayRedeemRequest is POST /admin/v1/billing/alipay/redemptions.
//
// AlipayRedeemRequest 是 POST /admin/v1/billing/alipay/redemptions 的请求。
type AlipayRedeemRequest struct {
	ActionID string `json:"action_id"`
	Amount   int64  `json:"amount"`
}

// AlipayRedemption is one redemption of Alipay quota into credits.
//
// AlipayRedemption 是一次把支付宝额度兑换为点数的记录。
type AlipayRedemption struct {
	ID           string    `json:"id"`
	ActionID     string    `json:"action_id"`
	Amount       int64     `json:"amount"`
	QuotaUnit    string    `json:"quota_unit"`
	CreditsMicro int64     `json:"credits_micro"`
	Status       string    `json:"status"`
	ReasonCode   string    `json:"reason_code"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// AlipayRedemptionListResponse is GET /admin/v1/billing/alipay/redemptions.
//
// AlipayRedemptionListResponse 是 GET /admin/v1/billing/alipay/redemptions 的响应。
type AlipayRedemptionListResponse struct {
	Items      []AlipayRedemption `json:"items"`
	NextCursor string             `json:"next_cursor"`
}
