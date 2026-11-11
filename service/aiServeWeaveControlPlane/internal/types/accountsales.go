package types

// AccountSale contains a credential-free monthly offer and its availability,
// priced per month in PriceMinor units of Currency. TenantID and
// DodoProductID are present only for operators; tenant lists carry Owned
// instead. Kind is claude or codex, empty for a legacy row. An archived row
// priced zero is kept only for presale assignment.
//
// AccountSale 是不含凭据的包月商品及其可用状态，月价为 Currency 的 PriceMinor 个最小
// 单位。TenantID 与 DodoProductID 只返回给运维；商户列表改用 Owned。Kind 为
// claude 或 codex，旧版商品为空字符串。价格为零的下架商品仅供预售分配。
type AccountSale struct {
	ID            string `json:"id"`
	NodeID        string `json:"node_id"`
	RuntimeID     string `json:"runtime_id"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	PriceMinor    int64  `json:"price_minor"`
	Currency      string `json:"currency"`
	DodoProductID string `json:"dodo_product_id,omitempty"`
	Active        bool   `json:"active"`
	Owned         bool   `json:"owned"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	TenantID      string `json:"tenant_id,omitempty"`
}

// AccountSaleRequest creates or updates an account. Kind is required on
// create; on update an empty kind keeps the stored one.
//
// AccountSaleRequest 创建或更新一个账号。创建时必须带 Kind；更新时为空表示保留已存类型。
type AccountSaleRequest struct {
	NodeID        string `json:"node_id"`
	RuntimeID     string `json:"runtime_id"`
	Kind          string `json:"kind"`
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

// AccountPresale is a presale of one month of an account of Kind, priced in
// PriceMinor units of Currency. Available tells a tenant whether it can be
// checked out now. DodoProductID, Capacity and Outstanding — the orders
// still waiting for an account, pending checkouts included — are present
// only for operators.
//
// AccountPresale 是某一类型账号一个月的预售，价格为 Currency 的 PriceMinor 个最小单位。
// Available 告诉商户此刻能否结账。DodoProductID、Capacity 与 Outstanding（仍在等待
// 账号的订单数，含待付款结账）只返回给运维。
type AccountPresale struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	PriceMinor    int64  `json:"price_minor"`
	Currency      string `json:"currency"`
	Active        bool   `json:"active"`
	Available     bool   `json:"available"`
	DodoProductID string `json:"dodo_product_id,omitempty"`
	Capacity      *int   `json:"capacity,omitempty"`
	Outstanding   *int   `json:"outstanding,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

// AccountPresaleRequest creates or updates a presale; kind cannot change.
//
// AccountPresaleRequest 创建或更新一个预售；类型不可更改。
type AccountPresaleRequest struct {
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	PriceMinor    int64  `json:"price_minor"`
	Currency      string `json:"currency"`
	DodoProductID string `json:"dodo_product_id"`
	Capacity      int    `json:"capacity"`
	Active        bool   `json:"active"`
}

// PresaleAssignRequest names the monthly account an operator gives a paid
// presale order.
//
// PresaleAssignRequest 指明运维分配给已付款预售订单的包月账号。
type PresaleAssignRequest struct {
	AccountID string `json:"account_id"`
}
