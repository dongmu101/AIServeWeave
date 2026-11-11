package model

import "time"

// AccountSale reserves one Agent runtime for exclusive monthly sale, priced in
// fiat and paid through a provider checkout. Archiving stops new sales; it
// never releases the runtime into the shared pool. Archived inventory may be
// unpriced and kept only for presale assignment. Kind is the target runtime's
// account kind; it is empty for legacy rows, which cannot be assigned to a
// presale until it is filled, and cannot change once set.
//
// AccountSale 为包月独享售卖保留一个运行时，以法币标价并经服务商结账付款。下架不影响
// 已付费期限，也不会恢复共享。PriceMinor 为零的商品来自旧版点数定价，须重新设定法币
// 价格后才能上架。下架的库存可以不标价，仅供分配给预售订单。Kind 是目标运行时的
// 账号类型（AccountKindClaude / AccountKindCodex），旧版商品为空，补填前不能用于预售
// 分配；一旦填写不可更改。
type AccountSale struct {
	ID            string    `gorm:"primaryKey;size:32"`
	NodeID        string    `gorm:"size:128;not null;uniqueIndex:idx_account_sales_target"`
	RuntimeID     string    `gorm:"size:128;not null;uniqueIndex:idx_account_sales_target"`
	Kind          string    `gorm:"size:16;not null"`
	Name          string    `gorm:"size:128;not null"`
	PriceMinor    int64     `gorm:"not null"`
	Currency      string    `gorm:"size:8;not null"`
	DodoProductID string    `gorm:"size:64;not null"`
	Active        bool      `gorm:"not null"`
	TenantID      string    `gorm:"size:32;not null"`
	ExpiresAt     time.Time `gorm:"not null"`
	UpdatedAt     time.Time `gorm:"not null"`
}

// AccountCheckoutHold is how long a pending checkout keeps a free account
// from other tenants' checkouts. A payment that settles after it still
// grants the month when the account is free; otherwise the order is left
// paid but ungranted for an operator refund.
//
// AccountCheckoutHold 是一笔待付款结账为本商户保留空闲账号、阻止其他商户结账的时长。
// 超过之后才完成的付款，账号仍空闲时照常开通；否则订单保持已付款未开通，由运维退款。
const AccountCheckoutHold = 30 * time.Minute

// MaxAccountPriceMinor bounds a monthly account price in minor currency
// units, e.g. 1,000,000.00 in a two-decimal currency.
//
// MaxAccountPriceMinor 是包月账号月价的上限（货币最小单位），例如两位小数币种的
// 1,000,000.00。
const MaxAccountPriceMinor = 100_000_000
