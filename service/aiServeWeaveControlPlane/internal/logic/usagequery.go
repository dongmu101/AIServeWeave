package logic

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"

	"AIServeWeave/common/pricing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// UsageCostsVisible reports whether usage reads carry costs: only when
// billing is on. With billing off nothing is priced, so a cost field would
// only ever read zero. See .scratch/token-pricing/spec.md「API」.
//
// UsageCostsVisible 报告用量读取是否携带费用：仅在开启计费时。计费关闭时不计价，费用
// 字段只会是零。见 .scratch/token-pricing/spec.md「API」。
func (s *Service) UsageCostsVisible() bool { return s.billingEnabled() }

// ListUsageRecords lists persisted usage records newest first. Like
// SummarizeUsage it makes no authorization decision of its own: the caller
// scopes filter.TenantID.
//
// ListUsageRecords 按最新优先列出已持久化的用量记录。与 SummarizeUsage 一样，它不做
// 自己的授权判断：由调用方限定 filter.TenantID。
func (s *Service) ListUsageRecords(ctx context.Context, query store.ListQuery, filter store.UsageRecordListFilter) (store.Page[model.UsageRecord], error) {
	page, err := s.store.ListUsageRecords(ctx, query, filter)
	return page, translate(err)
}

// UsageRecordDetails decodes a record's details. A record without details
// (written before the breakdown existed, or with none reported) and a
// record whose details do not decode get a tree built from its totals, so a
// reader always sees at least the input and output lines.
//
// UsageRecordDetails 解码一条记录的明细。没有明细的记录（写于细分出现之前，或没有上报
// 细分）以及明细无法解码的记录，按其总数构造计量项树，使读取方至少总能看到输入与输出
// 两行。
func UsageRecordDetails(r model.UsageRecord) model.UsageDetails {
	if r.Details != nil {
		var doc model.UsageDetails
		if json.Unmarshal([]byte(*r.Details), &doc) == nil && len(doc.Meters) > 0 {
			return doc
		}
	}
	doc := model.UsageDetails{V: model.UsageDetailsVersion}
	quantities, _ := pricing.Quantities(r.PromptTokens, r.CompletionTokens, nil)
	for _, name := range []string{pricing.MeterInput, pricing.MeterOutput} {
		if q, ok := quantities[name]; ok {
			doc.Meters = append(doc.Meters, model.UsageMeterLine{K: name, Q: q})
		}
	}
	return doc
}

// EffectivePrice is what an alias costs under one plan right now.
//
// EffectivePrice 是某个别名当前在一个方案下的价格。
type EffectivePrice struct {
	Model string
	// Routed is whether a published route serves the alias.
	// Routed 表示是否有已发布的模型路由服务该别名。
	Routed bool
	// Source is where the prices come from (model.PriceSource*); Ref is
	// the catalog entry when Source is catalog.
	// Source 是单价的来源（model.PriceSource*）；Source 为 catalog 时 Ref 是价目表条目。
	Source string
	Ref    string
	// ModelRatePPM and PlanRatePPM are the two rates, RatePPM their
	// product; a plan's fixed price shows all three at 1x.
	// ModelRatePPM 与 PlanRatePPM 是两个倍率，RatePPM 是它们的乘积；方案固定售价时三者
	// 都显示为 1 倍。
	ModelRatePPM int64
	PlanRatePPM  int64
	RatePPM      int64
	Items        []EffectivePriceItem
}

// EffectivePriceItem is one unit price: the base price and the price at
// the rates, rounded to the nearest micro-credit per million units.
//
// EffectivePriceItem 是一条单价：基础价，以及乘以倍率后的售价（四舍五入到每百万单位的
// micro 点）。
type EffectivePriceItem struct {
	Meter            string
	Variant          string
	AboveInputTokens int64
	BaseMicroPerM    int64
	SaleMicroPerM    int64
}

// EffectivePrices is the operator's view of every alias's prices under one
// plan, with what the unpriced-usage banner needs.
//
// EffectivePrices 是运维查看的、所有别名在一个方案下的价格，并附带未定价横幅所需的数据。
type EffectivePrices struct {
	Revision int64
	PlanID   string
	Items    []EffectivePrice
	// UnpricedAliases are the routed aliases nothing prices, and
	// UnpricedRequests24h the records charged at no price in the last 24
	// hours, across tenants.
	// UnpricedAliases 是没有任何单价的路由别名，UnpricedRequests24h 是最近 24 小时内
	// 跨租户以无单价计费的记录数。
	UnpricedAliases     []string
	UnpricedRequests24h int64
}

// unpricedWindow is how far back the unpriced-usage count looks.
//
// unpricedWindow 是未定价用量计数回看的时长。
const unpricedWindow = 24 * time.Hour

// OperatorEffectivePrices lists, for the operator, every routed alias and
// every alias configured in pricing, priced under planID (the default plan
// when empty) as of now. An unknown plan is ErrNotFound.
//
// OperatorEffectivePrices 为运维列出所有路由别名与所有在定价中配置过的别名，按 planID
// （为空时为默认方案）在当前时刻计价。方案不存在时返回 ErrNotFound。
func (s *Service) OperatorEffectivePrices(ctx context.Context, actor Actor, planID string) (EffectivePrices, error) {
	if err := s.requireBillingOperator(actor); err != nil {
		return EffectivePrices{}, err
	}
	snap, err := s.pricingSnapshot(ctx)
	if err != nil {
		return EffectivePrices{}, translate(err)
	}
	doc := snap.config.Document
	if planID == "" {
		planID = doc.DefaultPlan
	}
	if _, ok := doc.Plans[planID]; !ok {
		return EffectivePrices{}, ErrNotFound
	}
	routed, err := s.routedAliases(ctx)
	if err != nil {
		return EffectivePrices{}, err
	}
	aliases := map[string]bool{}
	for _, alias := range routed {
		aliases[alias] = true
	}
	for alias := range doc.Models {
		if _, seen := aliases[alias]; !seen {
			aliases[alias] = false
		}
	}
	out := EffectivePrices{Revision: snap.config.Revision.ID, PlanID: planID, Items: []EffectivePrice{}, UnpricedAliases: []string{}}
	now := s.clock.Now()
	for _, alias := range slices.Sorted(maps.Keys(aliases)) {
		price, err := s.effectivePrice(ctx, snap, alias, planID, now)
		if err != nil {
			return EffectivePrices{}, err
		}
		price.Routed = aliases[alias]
		if price.Routed && price.Source == model.PriceSourceNone {
			out.UnpricedAliases = append(out.UnpricedAliases, alias)
		}
		out.Items = append(out.Items, price)
	}
	if out.UnpricedRequests24h, err = s.store.CountUsageRecordsBySource(ctx, model.PriceSourceNone, now.Add(-unpricedWindow)); err != nil {
		return EffectivePrices{}, translate(err)
	}
	return out, nil
}

// TenantPrices lists authorized route aliases and configured direct model IDs
// from the tenant's filtered live inventory, priced under its current plan.
//
// TenantPrices 按租户当前方案为已授权的路由别名和已定价的直连模型计价。
// directModels 来自已按租户权限过滤的在服模型清单。
func (s *Service) TenantPrices(ctx context.Context, actor Actor, directModels []string) ([]EffectivePrice, error) {
	if !s.billingEnabled() {
		return nil, ErrNotFound
	}
	if actor.TenantID == "" || actor.TenantID == model.PlatformScope {
		return nil, ErrForbidden
	}
	policy, err := s.modelPolicy(ctx, actor.TenantID)
	if err != nil {
		return nil, err
	}
	snap, err := s.pricingSnapshot(ctx)
	if err != nil {
		return nil, translate(err)
	}
	now := s.clock.Now()
	plans, err := s.store.TenantPricePlansAt(ctx, []string{actor.TenantID}, now)
	if err != nil {
		return nil, translate(err)
	}
	routed, err := s.routedAliases(ctx)
	if err != nil {
		return nil, err
	}
	doc := snap.config.Document
	planID := plans[actor.TenantID]
	if _, known := doc.Plans[planID]; !known {
		planID = doc.DefaultPlan
	}
	planModels := doc.Plans[planID].Models
	aliases := make(map[string]bool, len(routed))
	for _, alias := range routed {
		aliases[alias] = true
	}
	for _, alias := range directModels {
		_, configured := doc.Models[alias]
		_, inPlan := planModels[alias]
		if !aliases[alias] && (configured || inPlan) {
			aliases[alias] = false
		}
	}

	out := []EffectivePrice{}
	for _, alias := range slices.Sorted(maps.Keys(aliases)) {
		if !policy.Allows(alias) {
			continue
		}
		price, err := s.effectivePrice(ctx, snap, alias, planID, now)
		if err != nil {
			return nil, err
		}
		price.Routed, price.Ref = aliases[alias], ""
		if !price.Routed && price.Source == model.PriceSourceNone {
			continue
		}
		out = append(out, price)
	}
	return out, nil
}

// defaultPlanPrices prices every routed alias under the default plan as of
// now, for the public catalog. Aliases nothing prices are left out rather
// than advertised as free.
//
// defaultPlanPrices 按默认方案在当前时刻为每个路由别名计价，供公开目录使用。没有任何
// 单价的别名不列出，而不是宣传为免费。
func (s *Service) defaultPlanPrices(ctx context.Context) ([]EffectivePrice, error) {
	snap, err := s.pricingSnapshot(ctx)
	if err != nil {
		return nil, translate(err)
	}
	routed, err := s.routedAliases(ctx)
	if err != nil {
		return nil, err
	}
	return s.publicPrices(ctx, snap, routed, "", s.clock.Now())
}

// publicPrices prices each routed alias under planID at the moment at, for a
// public list: aliases nothing prices are left out rather than advertised as
// free, and the catalog reference is dropped.
//
// publicPrices 按 planID 在 at 时刻为每个路由别名计价，供公开列表使用：没有任何单价的
// 别名不列出，而不是宣传为免费，并去掉价目表参照。
func (s *Service) publicPrices(ctx context.Context, snap *pricingSnapshot, routed []string, planID string, at time.Time) ([]EffectivePrice, error) {
	out := []EffectivePrice{}
	for _, alias := range routed {
		price, err := s.effectivePrice(ctx, snap, alias, planID, at)
		if err != nil {
			return nil, err
		}
		if price.Source == model.PriceSourceNone {
			continue
		}
		price.Routed, price.Ref = true, ""
		out = append(out, price)
	}
	return out, nil
}

// effectivePrice prices alias under planID at the moment at, with the same
// resolution charging uses.
//
// effectivePrice 以与扣费相同的解析方式，按 planID 在 at 时刻为 alias 计价。
func (s *Service) effectivePrice(ctx context.Context, snap *pricingSnapshot, alias, planID string, at time.Time) (EffectivePrice, error) {
	price, err := s.resolveUsagePrice(ctx, snap, alias, planID, at)
	if err != nil {
		return EffectivePrice{}, translate(err)
	}
	out := EffectivePrice{Model: alias, Source: price.source, Ref: price.ref, Items: []EffectivePriceItem{},
		ModelRatePPM: pricing.RateOne, PlanRatePPM: pricing.RateOne, RatePPM: pricing.CombinedRate(price.rates...)}
	if price.source != model.PriceSourcePlanFixed && len(price.rates) == 2 {
		out.ModelRatePPM, out.PlanRatePPM = price.rates[0], price.rates[1]
	}
	for _, item := range price.prices.Sorted() {
		out.Items = append(out.Items, EffectivePriceItem{
			Meter: item.Meter, Variant: item.Variant, AboveInputTokens: item.AboveInputTokens,
			BaseMicroPerM: item.PriceMicroPerM, SaleMicroPerM: pricing.ApplyRates(item.PriceMicroPerM, price.rates...),
		})
	}
	return out, nil
}

// routedAliases returns the aliases of the current model routes, and the
// video models the platform's Ark key allows (they are billed by the same
// name), sorted and without duplicates.
//
// routedAliases 返回当前模型路由的别名，以及平台方舟 Key 允许的视频模型（它们按同一名称
// 计费），已排序且去重。
func (s *Service) routedAliases(ctx context.Context) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	add := func(alias string) {
		if alias != "" && !seen[alias] {
			seen[alias] = true
			out = append(out, alias)
		}
	}
	routes, err := s.CurrentRoutes(ctx)
	switch {
	case err == nil:
		for _, route := range routes.Routes {
			add(route.Model)
		}
	case !errors.Is(err, ErrNotFound):
		return nil, err
	}
	videoModels, err := s.PlatformArkModels(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range videoModels {
		add(m)
	}
	slices.Sort(out)
	return out, nil
}
