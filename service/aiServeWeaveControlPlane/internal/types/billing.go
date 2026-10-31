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
	// RefundWindowDays is how long after a purchase the tenant may ask for a
	// refund; zero means it may not.
	//
	// RefundWindowDays 是购买后租户可申请退款的天数；为零表示不可申请。
	RefundWindowDays int `json:"refund_window_days"`
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
	ID            string `json:"id"`
	TenantID      string `json:"tenant_id,omitempty"`
	PackageID     string `json:"package_id"`
	AmountMinor   int64  `json:"amount_minor"`
	Currency      string `json:"currency"`
	CreditsMicro  int64  `json:"credits_micro"`
	RefundedMinor int64  `json:"refunded_minor"`
	// PaidAmountMinor and PaidCurrency are what the payment charged, tax
	// included; refunds are in these terms. Zero and empty until paid.
	//
	// PaidAmountMinor 与 PaidCurrency 是付款实际收取的金额（含税）；退款以它们为口径。
	// 付款之前为零值。
	PaidAmountMinor int64  `json:"paid_amount_minor"`
	PaidCurrency    string `json:"paid_currency"`
	// RefundableMinor, in PaidCurrency, is on the tenant's view what the
	// tenant may ask to refund now (window, unused credits, no refund in
	// progress), and on the operator's view what of the payment is not
	// refunded yet. Zero when nothing may be.
	//
	// RefundableMinor 以 PaidCurrency 计：在租户视图中是租户此刻可申请退款的金额
	// （期限内、未用点数、没有进行中的退款）；在运维视图中是付款中尚未退款的金额。不可
	// 退款时为零。
	RefundableMinor int64     `json:"refundable_minor"`
	Status          string    `json:"status"`
	PaymentID       string    `json:"payment_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
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

// RefundRequest is one refund of one order. AmountMinor is in Currency, the
// payment's, tax included; CreditsMicro is what approval held off the
// balance. ReviewedBy and TenantID appear on the operator's view only.
//
// RefundRequest 是一个订单的一次退款。AmountMinor 以付款币种 Currency 计，含税；
// CreditsMicro 是批准时从余额中冻结的点数。ReviewedBy 与 TenantID 只出现在运维视图中。
type RefundRequest struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenant_id,omitempty"`
	OrderID      string    `json:"order_id"`
	Source       string    `json:"source"`
	Status       string    `json:"status"`
	AmountMinor  int64     `json:"amount_minor"`
	Currency     string    `json:"currency"`
	CreditsMicro int64     `json:"credits_micro"`
	Reason       string    `json:"reason"`
	ReviewNote   string    `json:"review_note,omitempty"`
	Failure      string    `json:"failure,omitempty"`
	RefundID     string    `json:"refund_id,omitempty"`
	RequestedBy  string    `json:"requested_by"`
	ReviewedBy   string    `json:"reviewed_by,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// RefundRequestListResponse is one page of refund requests.
//
// RefundRequestListResponse 是一页退款申请。
type RefundRequestListResponse struct {
	Items      []RefundRequest `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

// RefundCreateRequest asks for a refund of an order: a tenant's request or
// an operator's direct refund. AmountMinor, in the payment's currency, is
// omitted to ask for everything allowed.
//
// RefundCreateRequest 请求对一个订单退款：租户的申请或运维的直接退款。AmountMinor 以
// 付款币种计，省略时表示申请允许的全部金额。
type RefundCreateRequest struct {
	AmountMinor int64  `json:"amount_minor,omitempty"`
	Reason      string `json:"reason"`
}

// RefundReviewRequest is an operator's answer to a refund request: approve
// (AmountMinor, when set, lowers the amount), reject, or mark failed, with
// Note as the remark.
//
// RefundReviewRequest 是运维对一个退款申请的答复：批准（AmountMinor 设置时调低金额）、
// 驳回或标记失败，Note 为备注。
type RefundReviewRequest struct {
	AmountMinor int64  `json:"amount_minor,omitempty"`
	Note        string `json:"note,omitempty"`
}
