package types

import "time"

// UsageRecordResponse is one request in GET /admin/v1/usage/records and
// /operator/v1/usage/records. The cost fields are present only with
// billing on; TenantID and each meter's catalog reference only on the
// operator endpoint. See .scratch/token-pricing/spec.md「API」.
//
// UsageRecordResponse 是 GET /admin/v1/usage/records 与 /operator/v1/usage/records
// 中的一次请求。费用字段只在开启计费时出现；TenantID 与价目表条目只在运维端点出现。见
// .scratch/token-pricing/spec.md「API」。
type UsageRecordResponse struct {
	RequestID        string            `json:"request_id"`
	TenantID         string            `json:"tenant_id,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	Model            string            `json:"model"`
	Endpoint         string            `json:"endpoint"`
	KeyID            string            `json:"key_id,omitempty"`
	UserID           string            `json:"user_id,omitempty"`
	PromptTokens     int64             `json:"prompt_tokens"`
	CompletionTokens int64             `json:"completion_tokens"`
	TotalTokens      int64             `json:"total_tokens"`
	CacheReadTokens  int64             `json:"cache_read_tokens"`
	CacheWriteTokens int64             `json:"cache_write_tokens"`
	Meters           []UsageMeterLine  `json:"meters"`
	Attributes       map[string]string `json:"attributes,omitempty"`
	// Adjusted is set when the reported counts were inconsistent and a
	// parent meter was raised to its children's sum.
	// Adjusted 在上报计数不一致、父项被提高到子项之和时设置。
	Adjusted bool `json:"adjusted,omitempty"`
	// The fields below appear only with billing on. PriceSource is empty
	// for a record charged before pricing, "legacy" for one backfilled.
	// 以下字段只在开启计费时出现。计价出现之前扣费的记录 PriceSource 为空，回填过的为
	// "legacy"。
	CostMicro     *int64 `json:"cost_micro,omitempty"`
	BaseCostMicro *int64 `json:"base_cost_micro,omitempty"`
	RatePPM       *int64 `json:"rate_ppm,omitempty"`
	PriceSource   string `json:"price_source,omitempty"`
	Charged       *bool  `json:"charged,omitempty"`
	Tier          int64  `json:"tier,omitempty"`
	OwnNode       bool   `json:"own_node,omitempty"`
	Unpriced      bool   `json:"unpriced,omitempty"`
	// Ref is the catalog entry the record was priced from; operator only.
	// Ref 是记录计价所依据的价目表条目；仅运维可见。
	Ref string `json:"ref,omitempty"`
}

// UsageMeterLine is one meter of a record: its quantity, the part its own
// price applies to and, with billing on, that base price and the line's
// cost at the record's rate.
//
// UsageMeterLine 是一条记录中的一个计量项：数量、适用其自身单价的部分，以及开启计费时的
// 基础单价与按记录倍率计算的该行费用。
type UsageMeterLine struct {
	Meter          string `json:"meter"`
	Quantity       int64  `json:"quantity"`
	Billed         int64  `json:"billed"`
	PriceMicroPerM *int64 `json:"price_micro_per_m,omitempty"`
	CostMicro      *int64 `json:"cost_micro,omitempty"`
}

// UsageRecordListResponse is one page of usage records.
//
// UsageRecordListResponse 是一页用量记录。
type UsageRecordListResponse struct {
	Items      []UsageRecordResponse `json:"items"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

// PriceView is one alias's prices. Ref, ModelRatePPM and PlanRatePPM are
// operator only; Routed is false for an alias only configured in pricing.
//
// PriceView 是一个别名的价格。Ref、ModelRatePPM 与 PlanRatePPM 仅运维可见；只在定价中
// 配置过的别名 Routed 为 false。
type PriceView struct {
	Model        string          `json:"model"`
	Routed       bool            `json:"routed"`
	Source       string          `json:"source"`
	Ref          string          `json:"ref,omitempty"`
	ModelRatePPM *int64          `json:"model_rate_ppm,omitempty"`
	PlanRatePPM  *int64          `json:"plan_rate_ppm,omitempty"`
	RatePPM      int64           `json:"rate_ppm"`
	Items        []PriceViewItem `json:"items"`
}

// PriceViewItem is one unit price, base and at the rates, in micro-credits
// per million units.
//
// PriceViewItem 是一条单价：基础价与乘以倍率后的售价，单位为 micro 点 / 百万单位。
type PriceViewItem struct {
	Meter            string `json:"meter"`
	Variant          string `json:"variant,omitempty"`
	AboveInputTokens int64  `json:"above_input_tokens,omitempty"`
	BaseMicroPerM    int64  `json:"base_micro_per_m"`
	SaleMicroPerM    int64  `json:"sale_micro_per_m"`
}

// EffectivePricesResponse is GET /operator/v1/billing/effective-prices.
// UnpricedAliases and UnpricedRequests24h feed the unpriced-usage banner.
//
// EffectivePricesResponse 是 GET /operator/v1/billing/effective-prices 的响应。
// UnpricedAliases 与 UnpricedRequests24h 供未定价横幅使用。
type EffectivePricesResponse struct {
	Revision            int64       `json:"revision"`
	PlanID              string      `json:"plan_id"`
	Items               []PriceView `json:"items"`
	UnpricedAliases     []string    `json:"unpriced_aliases"`
	UnpricedRequests24h int64       `json:"unpriced_requests_24h"`
}

// TenantPricesResponse is GET /admin/v1/billing/prices.
//
// TenantPricesResponse 是 GET /admin/v1/billing/prices 的响应。
type TenantPricesResponse struct {
	Items []PriceView `json:"items"`
}
