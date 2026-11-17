package types

import (
	"time"

	"AIServeWeave/common/pricing"
)

// PricingRevision is one version of the pricing configuration, without its
// document.
//
// PricingRevision 是定价配置的一个版本，不含文档。
type PricingRevision struct {
	ID          int64     `json:"id"`
	EffectiveAt time.Time `json:"effective_at"`
	CreatedBy   string    `json:"created_by"`
	Summary     string    `json:"summary"`
	CreatedAt   time.Time `json:"created_at"`
}

// PricingModel is one alias's settings. Prices is null when the alias has
// no override price; an empty catalog_model means the alias's own name.
//
// PricingModel 是一个别名的设置。没有覆盖价时 prices 为 null；catalog_model 为空表示用
// 别名本身。
type PricingModel struct {
	Model        string              `json:"model"`
	CatalogModel string              `json:"catalog_model"`
	Prices       []pricing.PriceItem `json:"prices"`
	RatePPM      int64               `json:"rate_ppm"`
}

// PricePlanModel is a plan's terms for one alias: exactly one of rate_ppm or
// prices (a fixed sale price).
//
// PricePlanModel 是方案对某个别名的条款：rate_ppm 与 prices（固定售价）恰好取其一。
type PricePlanModel struct {
	Model   string              `json:"model"`
	RatePPM *int64              `json:"rate_ppm,omitempty"`
	Prices  []pricing.PriceItem `json:"prices,omitempty"`
}

// PricePlan is one price plan. tenant_count is how many tenants are assigned
// to it, omitted for the default plan, which every unassigned tenant belongs
// to.
//
// PricePlan 是一个价格方案。tenant_count 是分配到它的租户数；默认方案省略该字段，因为
// 所有未分配的租户都属于它。
type PricePlan struct {
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	DefaultRatePPM int64            `json:"default_rate_ppm"`
	IsDefault      bool             `json:"is_default"`
	Models         []PricePlanModel `json:"models"`
	TenantCount    *int64           `json:"tenant_count,omitempty"`
}

// PricingConfigResponse is the whole current pricing configuration.
// default_prices is empty when there is no default price.
//
// PricingConfigResponse 是当前完整的定价配置。没有默认价时 default_prices 为空。
type PricingConfigResponse struct {
	Revision      PricingRevision     `json:"revision"`
	DefaultPrices []pricing.PriceItem `json:"default_prices"`
	Models        []PricingModel      `json:"models"`
	Plans         []PricePlan         `json:"plans"`
	DefaultPlan   string              `json:"default_plan"`
}

// PricingModelRequest replaces one alias's settings. The alias travels in
// the body because aliases may contain "/". An omitted rate_ppm is 1x.
// base_revision is the revision the operator edited.
//
// PricingModelRequest 替换一个别名的设置。别名放在请求体里，因为别名可能包含 "/"。
// 省略 rate_ppm 表示 1 倍。base_revision 是运维所编辑的那个版本。
type PricingModelRequest struct {
	Model        string              `json:"model"`
	CatalogModel string              `json:"catalog_model"`
	Prices       []pricing.PriceItem `json:"prices"`
	RatePPM      *int64              `json:"rate_ppm"`
	BaseRevision int64               `json:"base_revision"`
}

// DefaultPricesRequest replaces the default price; an empty list removes it.
//
// DefaultPricesRequest 替换默认价；空表表示去掉默认价。
type DefaultPricesRequest struct {
	Prices       []pricing.PriceItem `json:"prices"`
	BaseRevision int64               `json:"base_revision"`
}

// PricePlanRequest creates or replaces a plan. On update an omitted
// default_rate_ppm or models keeps the current value; on create they
// default to 1x and no per-alias terms.
//
// PricePlanRequest 新建或替换一个方案。更新时省略 default_rate_ppm 或 models 表示保持
// 原值；新建时它们分别默认为 1 倍与没有按别名的条款。
type PricePlanRequest struct {
	Name           string           `json:"name"`
	DefaultRatePPM *int64           `json:"default_rate_ppm"`
	Models         []PricePlanModel `json:"models"`
	BaseRevision   int64            `json:"base_revision"`
}

// PricePlanResponse is a saved plan and the revision that saved it.
//
// PricePlanResponse 是保存后的方案及保存它的版本。
type PricePlanResponse struct {
	Plan     PricePlan       `json:"plan"`
	Revision PricingRevision `json:"revision"`
}

// PricePlanListResponse is every plan as of one revision.
//
// PricePlanListResponse 是某个版本下的全部方案。
type PricePlanListResponse struct {
	Revision PricingRevision `json:"revision"`
	Items    []PricePlan     `json:"items"`
}

// PricingRevisionResponse carries the revision a deletion produced.
//
// PricingRevisionResponse 携带一次删除所产生的版本。
type PricingRevisionResponse struct {
	Revision PricingRevision `json:"revision"`
}

// PricingRevisionListResponse is one page of revisions, newest first.
//
// PricingRevisionListResponse 是一页版本，从新到旧排列。
type PricingRevisionListResponse struct {
	Items      []PricingRevision `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

// TenantPricePlanRequest moves a tenant to plan_id; null returns it to the
// default plan.
//
// TenantPricePlanRequest 让租户改属 plan_id；为 null 时回到默认方案。
type TenantPricePlanRequest struct {
	PlanID *string `json:"plan_id"`
}
