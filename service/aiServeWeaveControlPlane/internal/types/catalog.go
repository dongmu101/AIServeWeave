package types

// CatalogPackage is one credit package as the public sees it.
//
// CatalogPackage 是公众看到的一个充值包。
type CatalogPackage struct {
	ID                       string `json:"id"`
	Name                     string `json:"name"`
	Kind                     string `json:"kind"`
	CreditsMicro             int64  `json:"credits_micro,omitempty"`
	PriceMinor               int64  `json:"price_minor,omitempty"`
	Currency                 string `json:"currency"`
	CreditsPerMinorUnitMicro int64  `json:"credits_per_minor_unit_micro,omitempty"`
	MinAmountMinor           int64  `json:"min_amount_minor,omitempty"`
	MaxAmountMinor           int64  `json:"max_amount_minor,omitempty"`
}

// CatalogAccount is one monthly account on sale; it carries no node target.
//
// CatalogAccount 是一个在售的包月账号，不含节点目标。
type CatalogAccount struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PriceMicro int64  `json:"price_micro"`
}

// CatalogPrice is one alias's price per million tokens, in micro-credits.
//
// CatalogPrice 是一个别名每百万 token 的单价，单位为 micro 点。
type CatalogPrice struct {
	Model              string `json:"model"`
	InputMicroPerMTok  int64  `json:"input_micro_per_mtok"`
	OutputMicroPerMTok int64  `json:"output_micro_per_mtok"`
}

// CatalogResponse is GET /billing/v1/catalog.
//
// CatalogResponse 是 GET /billing/v1/catalog 的响应。
type CatalogResponse struct {
	Packages []CatalogPackage `json:"packages"`
	Accounts []CatalogAccount `json:"accounts"`
	Prices   []CatalogPrice   `json:"prices"`
}
