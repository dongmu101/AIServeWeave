package model

import "time"

// ID prefixes of the Alipay (一人付) records. Each id is also the
// idempotency key sent to 一人付 for its operation.
//
// 支付宝（一人付）记录的 ID 前缀。每个 id 同时也是该操作发往一人付的幂等编号。
const (
	PrefixAlipayPurchase   = "apu_"
	PrefixAlipayRedemption = "apr_"
)

// Alipay purchase entry statuses. Ready only means the entry was usable;
// nothing here records that a payment happened, as 一人付 reports none.
//
// 支付宝购买入口状态。ready 只表示入口曾经可用；这里没有任何状态表示付款已发生，因为
// 一人付不报告逐笔付款。
const (
	AlipayPurchaseUnknown = "unknown"
	AlipayPurchaseReady   = "ready"
	AlipayPurchaseFailed  = "failed"
	AlipayPurchaseExpired = "expired"
)

// Alipay redemption statuses. A pending redemption has been saved and may
// have been consumed in Alipay; it is resumed under its own id until it
// succeeds or definitely fails, never replaced by a new one.
//
// 支付宝兑换状态。pending 表示已保存、可能已在支付宝核销；它以自己的 id 恢复，直到成功
// 或明确失败，从不被新的兑换替代。
const (
	AlipayRedemptionPending   = "pending"
	AlipayRedemptionSucceeded = "succeeded"
	AlipayRedemptionFailed    = "failed"
)

// LedgerKindAlipayTopup credits a confirmed Alipay redemption.
//
// LedgerKindAlipayTopup 记入一次已确认的支付宝兑换。
const LedgerKindAlipayTopup = "alipay_topup"

// AlipayPurchase is one saved purchase intent: the parameters sent to
// 一人付 under ID, kept so a retry repeats them exactly. ActionID is the
// browser's id for one click, unique per tenant.
//
// AlipayPurchase 是一次已保存的购买意图：以 ID 为编号发往一人付的参数，保存下来使重试
// 原样重复。ActionID 是浏览器为一次点击生成的编号，在租户内唯一。
type AlipayPurchase struct {
	ID          string    `gorm:"primaryKey;size:32"`
	TenantID    string    `gorm:"size:32;not null;uniqueIndex:idx_alipay_purchases_action,priority:1"`
	ActionID    string    `gorm:"size:64;not null;uniqueIndex:idx_alipay_purchases_action,priority:2"`
	SkuID       string    `gorm:"size:128;not null"`
	CallbackURL string    `gorm:"size:512;not null"`
	Status      string    `gorm:"size:16;not null"`
	PurchaseURL string    `gorm:"size:2048;not null"`
	ExpireTime  string    `gorm:"size:64;not null"`
	ReasonCode  string    `gorm:"size:64;not null"`
	CreatedBy   string    `gorm:"size:32;not null"`
	CreatedAt   time.Time `gorm:"not null"`
	UpdatedAt   time.Time `gorm:"not null"`
}

// TableName pins the table name.
//
// TableName 钉死表名。
func (AlipayPurchase) TableName() string { return "alipay_purchases" }

// AlipayRedemption redeems Amount units of a tenant's Alipay quota into
// CreditsMicro credits, both fixed when it is created. ID is the
// consumption's idempotency key; the credits are booked under
// "alipay:<ID>" in the same transaction that marks it succeeded.
//
// AlipayRedemption 把租户支付宝额度中的 Amount 个单位兑换为 CreditsMicro 点数，两者在
// 创建时确定。ID 是核销的幂等编号；点数以 "alipay:<ID>" 为键，在把它标为成功的同一事务中入账。
type AlipayRedemption struct {
	ID             string `gorm:"primaryKey;size:32"`
	TenantID       string `gorm:"size:32;not null;uniqueIndex:idx_alipay_redemptions_action,priority:1"`
	ActionID       string `gorm:"size:64;not null;uniqueIndex:idx_alipay_redemptions_action,priority:2"`
	Amount         int64  `gorm:"not null"`
	QuotaUnit      string `gorm:"size:8;not null"`
	CreditsMicro   int64  `gorm:"not null"`
	Reason         string `gorm:"size:256;not null"`
	Status         string `gorm:"size:16;not null"`
	ReasonCode     string `gorm:"size:64;not null"`
	ConsumeOrderID string `gorm:"size:128;not null"`
	// PresaleOrderID is the presale order this redemption pays for; empty
	// for a redemption into credits. Such a redemption books no credits: its
	// success marks the order paid, its failure marks the order failed.
	//
	// PresaleOrderID 是这次兑换所支付的预售订单；兑换为点数时为空。这类兑换不入账点数：
	// 成功时把订单标为已付款，失败时把订单标为失败。
	PresaleOrderID string    `gorm:"size:32;not null"`
	CreatedBy      string    `gorm:"size:32;not null"`
	CreatedAt      time.Time `gorm:"not null"`
	UpdatedAt      time.Time `gorm:"not null"`
}

// TableName pins the table name.
//
// TableName 钉死表名。
func (AlipayRedemption) TableName() string { return "alipay_redemptions" }

// LedgerKeyAlipay is the ledger idempotency key of a redemption's credits.
//
// LedgerKeyAlipay 是一次兑换所入点数的账目幂等键。
func LedgerKeyAlipay(redemptionID string) string { return "alipay:" + redemptionID }
