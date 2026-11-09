package types

// AccountSale contains a credential-free monthly offer and its availability.
// TenantID is present only for operators; tenant lists carry Owned instead.
type AccountSale struct {
	ID         string `json:"id"`
	NodeID     string `json:"node_id"`
	RuntimeID  string `json:"runtime_id"`
	Name       string `json:"name"`
	PriceMicro int64  `json:"price_micro"`
	Active     bool   `json:"active"`
	Owned      bool   `json:"owned"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	TenantID   string `json:"tenant_id,omitempty"`
}

type AccountSaleRequest struct {
	NodeID     string `json:"node_id"`
	RuntimeID  string `json:"runtime_id"`
	Name       string `json:"name"`
	PriceMicro int64  `json:"price_micro"`
	Active     bool   `json:"active"`
}

type AccountPurchaseRequest struct {
	RequestID          string `json:"request_id"`
	ExpectedPriceMicro int64  `json:"expected_price_micro"`
}

type AccountPurchaseResponse struct {
	ID         string `json:"id"`
	AccountID  string `json:"account_id"`
	PriceMicro int64  `json:"price_micro"`
	ExpiresAt  string `json:"expires_at"`
}
