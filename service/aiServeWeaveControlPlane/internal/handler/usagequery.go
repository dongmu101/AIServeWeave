package handler

import (
	"net/http"
	"net/url"

	"AIServeWeave/common/pricing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// usageRecordFilterFrom reads the per-request list's shared parameters:
// since and until (RFC 3339, the same names the summary uses), model and
// key_id.
//
// usageRecordFilterFrom 读取逐笔列表共用的参数：since 与 until（RFC 3339，与汇总使用的
// 名称相同）、model 与 key_id。
func usageRecordFilterFrom(query url.Values) (store.UsageRecordListFilter, bool) {
	since, sinceOK := timeParam(query.Get("since"))
	until, untilOK := timeParam(query.Get("until"))
	if !sinceOK || !untilOK {
		return store.UsageRecordListFilter{}, false
	}
	return store.UsageRecordListFilter{Since: since, Until: until, Model: query.Get("model"), KeyID: query.Get("key_id")}, true
}

// renderUsageRecord converts one record. operator adds the tenant and the
// catalog reference; costs adds the pricing fields, which are left out with
// billing off. The details are decoded and re-assembled, never passed
// through, so a field added to the stored JSON later is not exposed by
// accident.
//
// renderUsageRecord 转换一条记录。operator 加上租户与价目表条目；costs 加上计价字段，
// 计费关闭时不出现。明细经解码后重新组装，从不原样透传，使以后加进存储 JSON 的字段不会
// 被意外暴露。
func renderUsageRecord(r model.UsageRecord, operator, costs bool) types.UsageRecordResponse {
	doc := logic.UsageRecordDetails(r)
	out := types.UsageRecordResponse{
		RequestID: r.ID, CreatedAt: r.CreatedAt, Model: r.Model, Endpoint: r.Endpoint, KeyID: r.KeyID, UserID: r.UserID,
		PromptTokens: r.PromptTokens, CompletionTokens: r.CompletionTokens, TotalTokens: r.TotalTokens,
		CacheReadTokens: r.CacheReadTokens, CacheWriteTokens: r.CacheWriteTokens,
		Attributes: doc.Attributes, Adjusted: doc.Adjusted, Meters: make([]types.UsageMeterLine, 0, len(doc.Meters)),
	}
	priced := costs && doc.Source != ""
	for _, line := range doc.Meters {
		billed := line.X
		if !priced && billed == 0 {
			billed = line.Q // unpriced records store no billed split / 未计价的记录不存计费数量
		}
		meter := types.UsageMeterLine{Meter: line.K, Quantity: line.Q, Billed: billed}
		if priced {
			meter.PriceMicroPerM, meter.CostMicro = int64Ptr(line.P), int64Ptr(line.C)
		}
		out.Meters = append(out.Meters, meter)
	}
	if operator {
		out.TenantID = r.TenantID
	}
	if costs {
		out.CostMicro, out.BaseCostMicro, out.RatePPM = int64Ptr(r.CostMicro), int64Ptr(r.BaseCostMicro), int64Ptr(r.RatePPM)
		out.PriceSource, out.Charged = r.PriceSource, r.Charged
		out.Tier, out.OwnNode, out.FreeNode, out.Unpriced = doc.Tier, doc.OwnNode, doc.FreeNode, doc.Unpriced
		if operator {
			out.Ref = doc.Ref
		}
	}
	return out
}

// listUsageRecordsTenant handles GET /admin/v1/usage/records, scoped to the
// caller's own tenant.
//
// listUsageRecordsTenant 处理 GET /admin/v1/usage/records，限定在调用方自己的租户。
func listUsageRecordsTenant(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		filter, ok := usageRecordFilterFrom(r.URL.Query())
		if !ok {
			writeError(w, http.StatusBadRequest, "since and until must be RFC 3339 timestamps")
			return
		}
		filter.TenantID = actor.TenantID
		respondUsageRecords(ctx, w, r, filter, false)
	}
}

// listUsageRecordsOperator handles GET /operator/v1/usage/records. An
// absent tenant_id lists every tenant, like the summary.
//
// listUsageRecordsOperator 处理 GET /operator/v1/usage/records。与汇总一样，缺少
// tenant_id 时列出所有租户。
func listUsageRecordsOperator(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, ok := usageRecordFilterFrom(r.URL.Query())
		if !ok {
			writeError(w, http.StatusBadRequest, "since and until must be RFC 3339 timestamps")
			return
		}
		filter.TenantID = r.URL.Query().Get("tenant_id")
		respondUsageRecords(ctx, w, r, filter, true)
	}
}

// respondUsageRecords reads one page and writes it.
//
// respondUsageRecords 读取一页并写出。
func respondUsageRecords(ctx *svc.ServiceContext, w http.ResponseWriter, r *http.Request, filter store.UsageRecordListFilter, operator bool) {
	page, err := ctx.Logic.ListUsageRecords(r.Context(), listQuery(r.URL.Query()), filter)
	if err != nil {
		respondErr(w, err)
		return
	}
	costs := ctx.Logic.UsageCostsVisible()
	out := make([]types.UsageRecordResponse, len(page.Items))
	for i, rec := range page.Items {
		out[i] = renderUsageRecord(rec, operator, costs)
	}
	writeJSON(w, http.StatusOK, types.UsageRecordListResponse{Items: out, NextCursor: page.NextCursor})
}

// renderPriceView converts one alias's prices; operator adds the catalog
// reference and the two rates.
//
// renderPriceView 转换一个别名的价格；operator 加上价目表条目与两个倍率。
func renderPriceView(p logic.EffectivePrice, operator bool) types.PriceView {
	out := types.PriceView{Model: p.Model, Routed: p.Routed, Source: p.Source, RatePPM: p.RatePPM, Items: make([]types.PriceViewItem, len(p.Items))}
	for i, item := range p.Items {
		out.Items[i] = types.PriceViewItem{Meter: item.Meter, Variant: item.Variant, AboveInputTokens: item.AboveInputTokens,
			BaseMicroPerM: item.BaseMicroPerM, SaleMicroPerM: item.SaleMicroPerM}
	}
	if operator {
		out.Ref, out.ModelRatePPM, out.PlanRatePPM = p.Ref, int64Ptr(p.ModelRatePPM), int64Ptr(p.PlanRatePPM)
	}
	return out
}

// getEffectivePrices handles GET /operator/v1/billing/effective-prices,
// with an optional plan_id.
//
// getEffectivePrices 处理 GET /operator/v1/billing/effective-prices，plan_id 可选。
func getEffectivePrices(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, _ := actorFrom(r.Context())
		prices, err := ctx.Logic.OperatorEffectivePrices(r.Context(), actor, r.URL.Query().Get("plan_id"))
		if err != nil {
			respondErr(w, err)
			return
		}
		out := types.EffectivePricesResponse{Revision: prices.Revision, PlanID: prices.PlanID, Items: make([]types.PriceView, len(prices.Items)),
			UnpricedAliases: prices.UnpricedAliases, UnpricedRequests24h: prices.UnpricedRequests24h}
		for i, p := range prices.Items {
			out.Items[i] = renderPriceView(p, true)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// getTenantPrices handles GET /admin/v1/billing/prices.
//
// getTenantPrices 处理 GET /admin/v1/billing/prices。
func getTenantPrices(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		prices, err := ctx.Logic.TenantPrices(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		out := types.TenantPricesResponse{Items: make([]types.PriceView, len(prices))}
		for i, p := range prices {
			out.Items[i] = renderPriceView(p, false)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// catalogPrice converts one alias's default-plan prices for the public
// catalog: the base-tier input and output sale prices the pricing page has
// always shown, plus every sale price.
//
// catalogPrice 为公开目录转换一个别名在默认方案下的价格：价目页一直显示的基础档输入与
// 输出售价，加上全部售价。
func catalogPrice(p logic.EffectivePrice) types.CatalogPrice {
	sale := make(pricing.PriceList, len(p.Items))
	out := types.CatalogPrice{Model: p.Model, Items: make([]types.CatalogPriceItem, len(p.Items))}
	for i, item := range p.Items {
		sale[i] = pricing.PriceItem{Meter: item.Meter, Variant: item.Variant, AboveInputTokens: item.AboveInputTokens, PriceMicroPerM: item.SaleMicroPerM}
		out.Items[i] = types.CatalogPriceItem{Meter: item.Meter, Variant: item.Variant, AboveInputTokens: item.AboveInputTokens, PriceMicroPerM: item.SaleMicroPerM}
	}
	out.InputMicroPerMTok, _ = pricing.Lookup(sale, pricing.MeterInput, "", 0)
	out.OutputMicroPerMTok, _ = pricing.Lookup(sale, pricing.MeterOutput, "", 0)
	return out
}

// int64Ptr returns a pointer to v.
//
// int64Ptr 返回指向 v 的指针。
func int64Ptr(v int64) *int64 { return &v }
