package types

import "time"

// Money and credits cross this API as integers: credits in micro-credits
// (one million to a credit) and money in the currency's minor unit. Both stay
// below 2^53 for any real balance, so a JavaScript client reads them exactly.
//
// 金额与点数以整数形式穿过本 API：点数以 micro 点为单位（一百万为一点），金额以货币
// 最小单位计。任何真实余额都低于 2^53，因此 JavaScript 客户端能精确读取。

// CreditPackage is one purchasable package.
//
// CreditPackage 是一个可购买的充值包。
type CreditPackage struct {
	ID                       string    `json:"id"`
	Name                     string    `json:"name"`
	Kind                     string    `json:"kind"`
	CreditsMicro             int64     `json:"credits_micro,omitempty"`
	PriceMinor               int64     `json:"price_minor,omitempty"`
	Currency                 string    `json:"currency"`
	CreditsPerMinorUnitMicro int64     `json:"credits_per_minor_unit_micro,omitempty"`
	MinAmountMinor           int64     `json:"min_amount_minor,omitempty"`
	MaxAmountMinor           int64     `json:"max_amount_minor,omitempty"`
	Active                   bool      `json:"active"`
	SortOrder                int64     `json:"sort_order"`
	DodoProductID            string    `json:"dodo_product_id,omitempty"`
	CreatedAt                time.Time `json:"created_at"`
	UpdatedAt                time.Time `json:"updated_at"`
}

// BillingOverviewResponse is GET /admin/v1/billing.
//
// BillingOverviewResponse 是 GET /admin/v1/billing 的响应。
type BillingOverviewResponse struct {
	Mode         string          `json:"mode"`
	BalanceMicro int64           `json:"balance_micro"`
	Blocked      bool            `json:"blocked"`
	HasCustomer  bool            `json:"has_customer"`
	Packages     []CreditPackage `json:"packages"`
}

// CheckoutRequest is POST /admin/v1/billing/checkout. AmountMinor is the
// chosen amount for a custom package and omitted for a fixed one.
//
// CheckoutRequest 是 POST /admin/v1/billing/checkout 的请求。AmountMinor 是自定义
// 充值包所选的金额，固定充值包省略。
type CheckoutRequest struct {
	PackageID   string `json:"package_id"`
	AmountMinor int64  `json:"amount_minor,omitempty"`
}

// CheckoutResponse carries the hosted checkout page to send the tenant to.
//
// CheckoutResponse 携带要把租户送往的托管结账页面。
type CheckoutResponse struct {
	OrderID     string `json:"order_id"`
	CheckoutURL string `json:"checkout_url"`
}

// PortalResponse carries a one-time customer portal link.
//
// PortalResponse 携带一次性的客户门户链接。
type PortalResponse struct {
	URL string `json:"url"`
}

// LedgerEntry is one balance change. TenantID is omitted on the tenant's own
// view.
//
// LedgerEntry 是一次余额变动。租户自己的视图省略 TenantID。
type LedgerEntry struct {
	ID                string    `json:"id"`
	TenantID          string    `json:"tenant_id,omitempty"`
	Kind              string    `json:"kind"`
	AmountMicro       int64     `json:"amount_micro"`
	BalanceAfterMicro int64     `json:"balance_after_micro"`
	Ref               string    `json:"ref"`
	CreatedAt         time.Time `json:"created_at"`
}

// LedgerListResponse is one page of ledger entries.
//
// LedgerListResponse 是一页账目。
type LedgerListResponse struct {
	Items      []LedgerEntry `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// CheckoutOrder is one purchase attempt. TenantID is omitted on the tenant's
// own view.
//
// CheckoutOrder 是一次购买尝试。租户自己的视图省略 TenantID。
type CheckoutOrder struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id,omitempty"`
	PackageID     string    `json:"package_id"`
	AmountMinor   int64     `json:"amount_minor"`
	Currency      string    `json:"currency"`
	CreditsMicro  int64     `json:"credits_micro"`
	RefundedMinor int64     `json:"refunded_minor"`
	Status        string    `json:"status"`
	PaymentID     string    `json:"payment_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// OrderListResponse is one page of orders.
//
// OrderListResponse 是一页订单。
type OrderListResponse struct {
	Items      []CheckoutOrder `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

// CreditPackageRequest creates or replaces a package.
//
// CreditPackageRequest 创建或替换一个充值包。
type CreditPackageRequest struct {
	Name                     string `json:"name"`
	Kind                     string `json:"kind"`
	DodoProductID            string `json:"dodo_product_id"`
	CreditsMicro             int64  `json:"credits_micro"`
	PriceMinor               int64  `json:"price_minor"`
	Currency                 string `json:"currency"`
	CreditsPerMinorUnitMicro int64  `json:"credits_per_minor_unit_micro"`
	MinAmountMinor           int64  `json:"min_amount_minor"`
	MaxAmountMinor           int64  `json:"max_amount_minor"`
	Active                   bool   `json:"active"`
	SortOrder                int64  `json:"sort_order"`
}

// CreditPackageListResponse is every package.
//
// CreditPackageListResponse 是全部充值包。
type CreditPackageListResponse struct {
	Items []CreditPackage `json:"items"`
}

// ModelPrice is one alias's price per million tokens, in micro-credits.
//
// ModelPrice 是一个别名每百万 token 的单价，单位为 micro 点。
type ModelPrice struct {
	Model              string    `json:"model"`
	InputMicroPerMTok  int64     `json:"input_micro_per_mtok"`
	OutputMicroPerMTok int64     `json:"output_micro_per_mtok"`
	UpdatedBy          string    `json:"updated_by"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// ModelPriceListResponse is every price.
//
// ModelPriceListResponse 是全部单价。
type ModelPriceListResponse struct {
	Items []ModelPrice `json:"items"`
}

// ModelPriceRequest sets one alias's price.
//
// ModelPriceRequest 设置一个别名的单价。
type ModelPriceRequest struct {
	Model              string `json:"model"`
	InputMicroPerMTok  int64  `json:"input_micro_per_mtok"`
	OutputMicroPerMTok int64  `json:"output_micro_per_mtok"`
}

// BillingTenant is one tenant with its billing state.
//
// BillingTenant 是一个租户及其计费状态。
type BillingTenant struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	Mode         string    `json:"mode"`
	BalanceMicro int64     `json:"balance_micro"`
	Blocked      bool      `json:"blocked"`
	CreatedAt    time.Time `json:"created_at"`
}

// BillingTenantListResponse is one page of tenants with billing state.
//
// BillingTenantListResponse 是一页带计费状态的租户。
type BillingTenantListResponse struct {
	Items      []BillingTenant `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

// BillingModeRequest sets a tenant's billing mode.
//
// BillingModeRequest 设置租户的计费模式。
type BillingModeRequest struct {
	Mode string `json:"mode"`
}

// CreditAdjustRequest adds AmountMicro (negative to deduct) to a tenant's
// balance; Reason is recorded on the ledger entry.
//
// CreditAdjustRequest 把 AmountMicro（为负则扣减）记入租户余额；Reason 记录在账目上。
type CreditAdjustRequest struct {
	AmountMicro int64  `json:"amount_micro"`
	Reason      string `json:"reason"`
}
