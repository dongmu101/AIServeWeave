package types

// AccountSale contains a credential-free monthly offer and its availability,
// priced per month in PriceMinor units of Currency. TenantID and
// DodoProductID are present only for operators; tenant lists carry Owned
// instead.
//
// AccountSale 是不含凭据的包月商品及其可用状态，月价为 Currency 的 PriceMinor 个最小
// 单位。TenantID 与 DodoProductID 只返回给运维；商户列表改用 Owned。
type AccountSale struct {
	ID            string `json:"id"`
	NodeID        string `json:"node_id"`
	RuntimeID     string `json:"runtime_id"`
	Name          string `json:"name"`
	PriceMinor    int64  `json:"price_minor"`
	Currency      string `json:"currency"`
	DodoProductID string `json:"dodo_product_id,omitempty"`
	Active        bool   `json:"active"`
	Owned         bool   `json:"owned"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	TenantID      string `json:"tenant_id,omitempty"`
}

type AccountSaleRequest struct {
	NodeID        string `json:"node_id"`
	RuntimeID     string `json:"runtime_id"`
	Name          string `json:"name"`
	PriceMinor    int64  `json:"price_minor"`
	Currency      string `json:"currency"`
	DodoProductID string `json:"dodo_product_id"`
	Active        bool   `json:"active"`
}

// AccountCheckoutRequest opens a checkout for one month at the price the
// tenant saw; a different current price is a conflict.
//
// AccountCheckoutRequest 以商户看到的价格为一个月发起结账；当前价格不同则冲突。
type AccountCheckoutRequest struct {
	ExpectedPriceMinor int64  `json:"expected_price_minor"`
	Currency           string `json:"currency"`
}
