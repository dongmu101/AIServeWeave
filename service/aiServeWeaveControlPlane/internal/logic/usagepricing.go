package logic

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"AIServeWeave/common/nodeaccess"
	"AIServeWeave/common/pricing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// usagePricingCache holds the pricing data in effect from a moment on: the
// latest pricing configuration revision and the current catalog. Charging
// reads the two version numbers on every batch and reloads only when one
// changed, so every replica prices with the latest data without waiting for
// a refresh, and a batch normally costs two small reads.
//
// usagePricingCache 保存从某一刻起生效的计价数据：最新的定价配置版本与当前的价目表。
// 扣费在每一批都读取两个版本号，只有其中一个变化时才重新加载，因此每个副本都用最新的
// 数据计价、无需等待刷新，一批通常只多两次小查询。
type usagePricingCache struct {
	mu   sync.Mutex
	snap *pricingSnapshot
}

// pricingSnapshot is one load of usagePricingCache.
//
// pricingSnapshot 是 usagePricingCache 的一次加载结果。
type pricingSnapshot struct {
	configVersion  int64
	catalogVersion int64
	config         PricingConfigView
	catalog        map[string]pricing.PriceList
	// catalogChangedAt is when the catalog last changed: from then on,
	// catalog is the catalog in effect, absent entries included.
	// catalogChangedAt 是价目表最后一次变化的时刻：从那时起，catalog 就是生效中的价目表，
	// 没有的条目也确实没有单价。
	catalogChangedAt time.Time
}

// pricingSnapshot returns the cached pricing data, reloading it when either
// version number moved. Each version is read before the data it guards, so
// a change landing during the load leaves an older version on the snapshot
// and is picked up by the next batch, never the reverse.
//
// pricingSnapshot 返回缓存的计价数据，任一版本号变化时重新加载。每个版本号都在它所
// 守护的数据之前读取，因此加载期间发生的变化只会让快照带着较旧的版本号，由下一批重新
// 加载，而不会反过来。
func (s *Service) pricingSnapshot(ctx context.Context) (*pricingSnapshot, error) {
	configVersion, err := s.store.PricingConfigVersion(ctx)
	if err != nil {
		return nil, err
	}
	catalogVersion := int64(0)
	state, err := s.store.CatalogSyncState(ctx)
	switch {
	case err == nil:
		catalogVersion = state.Version
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}
	cache := s.pricingCache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if snap := cache.snap; snap != nil && snap.configVersion == configVersion && snap.catalogVersion == catalogVersion {
		return snap, nil
	}
	config, err := s.EnsurePricingConfig(ctx)
	if err != nil {
		return nil, err
	}
	set, err := s.store.CurrentCatalogPrices(ctx)
	if err != nil {
		return nil, err
	}
	snap := &pricingSnapshot{
		configVersion: configVersion, catalogVersion: set.Version, config: config,
		catalog: make(map[string]pricing.PriceList, len(set.Prices)), catalogChangedAt: set.ChangedAt,
	}
	for name, rows := range set.Prices {
		snap.catalog[name] = catalogPriceList(rows)
	}
	cache.snap = snap
	return snap, nil
}

// catalogPriceList converts catalog rows to a price list.
//
// catalogPriceList 把价目表行转换为单价表。
func catalogPriceList(rows []model.CatalogPrice) pricing.PriceList {
	out := make(pricing.PriceList, 0, len(rows))
	for _, row := range rows {
		out = append(out, pricing.PriceItem{Meter: row.Meter, Variant: row.Variant, AboveInputTokens: row.AboveInputTokens, PriceMicroPerM: row.PriceMicroPerM})
	}
	return out
}

// usagePrice is where one record's prices come from and the rates on them.
//
// usagePrice 是一条记录的单价来源及其倍率。
type usagePrice struct {
	prices   pricing.PriceList
	source   string
	ref      string
	rates    []int64
	revision int64
	plan     string
}

// resolveUsagePrice picks the prices for alias on planID's terms as they
// stood at the moment at, following .scratch/token-pricing/spec.md「定价与
// 价格方案」: a plan's fixed price as is; otherwise the alias's override, its
// catalog entry (the reference model, or its own name, matched exactly) or
// the default price, at the model rate times the plan factor.
//
// resolveUsagePrice 按 at 时刻的情况，为 alias 选出 planID 条款下的单价，规则见
// .scratch/token-pricing/spec.md「定价与价格方案」：方案的固定售价原样使用；否则依次取
// 该别名的覆盖价、其价目表条目（参照模型或自身名称，完全一致才算命中）或默认价，倍率为
// 模型倍率乘以方案系数。
func (s *Service) resolveUsagePrice(ctx context.Context, snap *pricingSnapshot, alias, planID string, at time.Time) (usagePrice, error) {
	view := snap.config
	if at.Before(view.Revision.EffectiveAt) {
		older, err := s.PricingConfigAt(ctx, at)
		if err != nil {
			return usagePrice{}, err
		}
		view = older
	}
	doc := view.Document
	if _, known := doc.Plans[planID]; planID == "" || !known {
		planID = doc.DefaultPlan
	}
	out := usagePrice{revision: view.Revision.ID, plan: planID}
	planRate, fixed := doc.PlanTerms(planID, alias)
	if len(fixed) > 0 {
		out.prices, out.source, out.rates = fixed, model.PriceSourcePlanFixed, []int64{pricing.RateOne}
		return out, nil
	}
	out.rates = []int64{doc.ModelRate(alias), planRate}
	cfg, configured := doc.Models[alias]
	if configured && len(cfg.Prices) > 0 {
		out.prices, out.source = cfg.Prices, model.PriceSourceOverride
		return out, nil
	}
	name := alias
	if configured && cfg.CatalogModel != "" {
		name = cfg.CatalogModel
	}
	prices, found, err := s.catalogPricesAt(ctx, snap, name, at)
	if err != nil {
		return usagePrice{}, err
	}
	switch {
	case found:
		out.prices, out.source, out.ref = prices, model.PriceSourceCatalog, name
	case len(doc.DefaultPrices) > 0:
		out.prices, out.source = doc.DefaultPrices, model.PriceSourceDefault
	default:
		out.source = model.PriceSourceNone
	}
	return out, nil
}

// catalogPricesAt returns entry name's prices at the moment at: from the
// snapshot when at is no earlier than the catalog's last change, otherwise
// from the store's history.
//
// catalogPricesAt 返回条目 name 在 at 时刻的单价：at 不早于价目表最后一次变化时取自
// 快照，否则取自 store 的历史。
func (s *Service) catalogPricesAt(ctx context.Context, snap *pricingSnapshot, name string, at time.Time) (pricing.PriceList, bool, error) {
	if !at.Before(snap.catalogChangedAt) {
		prices, ok := snap.catalog[name]
		return prices, ok, nil
	}
	rows, err := s.store.CatalogPricesAt(ctx, name, at)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return catalogPriceList(rows), true, nil
}

// tenantPlansAt returns, for each record, its tenant's price plan at the
// moment it is priced at ("" for the default plan). A batch spans seconds,
// so the plans at its earliest and latest moments are read once each; only
// a tenant whose plan changed in between is read record by record.
//
// tenantPlansAt 为每条记录返回其租户在计价时刻所属的价格方案（"" 表示默认方案）。
// 一批只跨几秒，因此只在最早与最晚的时刻各读一次；只有在这期间换过方案的租户才逐条读取。
func (s *Service) tenantPlansAt(ctx context.Context, records []model.UsageRecord, moments []time.Time) ([]string, error) {
	out := make([]string, len(records))
	if len(records) == 0 {
		return out, nil
	}
	tenants := make([]string, 0, len(records))
	seen := map[string]bool{}
	earliest, latest := moments[0], moments[0]
	for i, r := range records {
		if !seen[r.TenantID] {
			seen[r.TenantID] = true
			tenants = append(tenants, r.TenantID)
		}
		if moments[i].Before(earliest) {
			earliest = moments[i]
		}
		if moments[i].After(latest) {
			latest = moments[i]
		}
	}
	first, err := s.store.TenantPricePlansAt(ctx, tenants, earliest)
	if err != nil {
		return nil, err
	}
	last := first
	if latest.After(earliest) {
		if last, err = s.store.TenantPricePlansAt(ctx, tenants, latest); err != nil {
			return nil, err
		}
	}
	for i, r := range records {
		if first[r.TenantID] == last[r.TenantID] {
			out[i] = first[r.TenantID]
			continue
		}
		plans, err := s.store.TenantPricePlansAt(ctx, []string{r.TenantID}, moments[i])
		if err != nil {
			return nil, err
		}
		out[i] = plans[r.TenantID]
	}
	return out, nil
}

// chargeUsage prices records and charges them in one store transaction,
// dropping cached verifications when a tenant's balance ran out. Each record
// is priced as things stood when the request was made (its created_at, or
// now for a timestamp in the future), so it does not matter which replica
// takes the batch or how late. A request served by the tenant's own node is
// priced but costs nothing.
//
// A pricing change commits a moment after it takes effect; a record made in
// that window and pushed before the commit is priced at the old prices. The
// window is the length of one small transaction.
//
// chargeUsage 为 records 计价并在一次 store 事务中扣费；租户余额耗尽时丢弃已缓存的
// 校验结果。每条记录都按发起请求时（其 created_at；时间在未来时按当前时间）的情况计价，
// 因此由哪个副本、多晚处理这批都没有关系。由商户自己的节点服务的请求照常计价，但不收费。
//
// 定价变更在生效时刻之后片刻才提交；在这段时间内发起、并在提交前推送的记录按旧价计价。
// 这段时间只有一次小事务那么长。
func (s *Service) chargeUsage(ctx context.Context, records []model.UsageRecord) error {
	now := s.clock.Now()
	snap, err := s.pricingSnapshot(ctx)
	if err != nil {
		return err
	}
	moments := make([]time.Time, len(records))
	for i, r := range records {
		moments[i] = r.CreatedAt
		if moments[i].After(now) {
			moments[i] = now
		}
	}
	plans, err := s.tenantPlansAt(ctx, records, moments)
	if err != nil {
		return err
	}
	free, err := s.freeNodes(ctx, records)
	if err != nil {
		return err
	}
	for i := range records {
		r := &records[i]
		price, err := s.resolveUsagePrice(ctx, snap, r.Model, plans[i], moments[i])
		if err != nil {
			return err
		}
		// No service tier is priced yet, so every record uses the default
		// variant; Compute already falls back to it for any other.
		// 目前还没有为服务等级定价，因此每条记录都使用默认变体；对其他变体 Compute 也会
		// 回落到它。
		bill := pricing.Compute(r.PromptTokens, r.CompletionTokens, r.ReportedDetails, price.prices, "", price.rates...)
		r.BaseCostMicro, r.CostMicro = bill.BaseCostMicro, bill.CostMicro
		r.RatePPM, r.PriceSource = pricing.CombinedRate(price.rates...), price.source
		doc := model.UsageDetails{
			V: model.UsageDetailsVersion, Attributes: r.ReportedAttributes, Adjusted: bill.Adjusted,
			Revision: price.revision, Plan: price.plan, Source: price.source, Ref: price.ref,
			Tier: bill.Tier, Rate: r.RatePPM, Unpriced: bill.Unpriced,
			Meters: make([]model.UsageMeterLine, 0, len(bill.Lines)),
		}
		for _, line := range bill.Lines {
			doc.Meters = append(doc.Meters, model.UsageMeterLine{K: line.Meter, Q: line.Quantity, X: line.Billed, P: line.Price, C: line.Cost})
		}
		// A tenant's own hardware is not billed; the record still carries
		// what it would have cost.
		// 商户自己的硬件不计费；记录仍保留本应产生的费用明细。
		if owner, owned := nodeaccess.OwnerOf(r.NodeID); owned && owner == r.TenantID {
			r.CostMicro, doc.OwnNode = 0, true
		} else if r.ReportedAttributes[pricing.AttrCredential] == pricing.CredentialTenant {
			// The tenant's own third-party key paid for this, as its own
			// hardware does for a node.
			// 这次请求由租户自己的第三方 Key 付费，与自有节点由租户自己的硬件承担同理。
			r.CostMicro, doc.OwnKey = 0, true
		} else if free[r.NodeID] {
			// An operator made this platform node free; the record keeps
			// what it would have cost, as for an own node.
			// 运维把这台平台节点设为免费；与自有节点相同，记录保留本应产生的费用明细。
			r.CostMicro, doc.FreeNode = 0, true
		}
		encoded, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		text := string(encoded)
		r.Details = &text
	}
	changed, err := s.store.ChargeUsage(ctx, records, now, store.NextLocalMidnight(now, s.ledgerLocation()))
	if err != nil {
		return err
	}
	if changed && s.invalidator != nil {
		s.invalidator.InvalidateAll(ctx)
	}
	return nil
}

// ledgerLocation is the time zone of the usage ledger's days.
//
// ledgerLocation 是用量账目日界所用的时区。
func (s *Service) ledgerLocation() *time.Location {
	if s.billing == nil || s.billing.LedgerLocation == nil {
		return time.UTC
	}
	return s.billing.LedgerLocation
}
