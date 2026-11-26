package types

import "time"

// PublicPlanPrices is one price plan as the public sees it: its id and name
// and the routed aliases' sale prices under it, in the catalog's shape. It
// carries no rates, catalog references or tenant counts.
//
// PublicPlanPrices 是公众看到的一个价格方案：id、名称，以及路由别名在该方案下的售价，
// 形状与目录相同。不含倍率、价目表参照或租户数。
type PublicPlanPrices struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Default bool           `json:"default"`
	Prices  []CatalogPrice `json:"prices"`
}

// PublicPlanPricesResponse is GET /billing/v1/plan-prices. UpdatedAt is when
// a sale price last could have changed; absent when unknown.
//
// PublicPlanPricesResponse 是 GET /billing/v1/plan-prices 的响应。UpdatedAt 是售价最后
// 一次可能变化的时刻；未知时不发送。
type PublicPlanPricesResponse struct {
	UpdatedAt *time.Time         `json:"updated_at,omitempty"`
	Plans     []PublicPlanPrices `json:"plans"`
}
