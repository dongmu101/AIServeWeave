package handler

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/common/pricing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/pricingconfig"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// getPricingConfig handles GET /operator/v1/billing/pricing.
//
// getPricingConfig 处理 GET /operator/v1/billing/pricing。
func getPricingConfig(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		view, err := ctx.Logic.GetPricingConfig(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, renderPricingConfig(view))
	})
}

// putPricingModel handles PUT /operator/v1/billing/pricing/models. The alias
// travels in the body because aliases may contain "/".
//
// putPricingModel 处理 PUT /operator/v1/billing/pricing/models。别名放在请求体里，因为
// 别名可能包含 "/"。
func putPricingModel(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		var req types.PricingModelRequest
		if !decode(w, r, &req) {
			return
		}
		view, err := ctx.Logic.PutPricingModel(r.Context(), actor, logic.PricingModelInput{
			Model: req.Model, CatalogModel: req.CatalogModel, Prices: req.Prices, RatePPM: req.RatePPM, BaseRevision: req.BaseRevision,
		})
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, renderPricingConfig(view))
	})
}

// deletePricingModel handles
// DELETE /operator/v1/billing/pricing/models?model=&base_revision=, with
// the alias in the query for the reason putPricingModel gives.
//
// deletePricingModel 处理
// DELETE /operator/v1/billing/pricing/models?model=&base_revision=，别名放在查询参数
// 里，理由见 putPricingModel。
func deletePricingModel(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		query := r.URL.Query()
		base, ok := baseRevisionParam(w, query.Get("base_revision"))
		if !ok {
			return
		}
		view, err := ctx.Logic.DeletePricingModel(r.Context(), actor, query.Get("model"), base)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, renderPricingConfig(view))
	})
}

// putDefaultPrices handles PUT /operator/v1/billing/pricing/default-prices.
//
// putDefaultPrices 处理 PUT /operator/v1/billing/pricing/default-prices。
func putDefaultPrices(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		var req types.DefaultPricesRequest
		if !decode(w, r, &req) {
			return
		}
		view, err := ctx.Logic.PutDefaultPrices(r.Context(), actor, req.Prices, req.BaseRevision)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, renderPricingConfig(view))
	})
}

// listPricingRevisions handles GET /operator/v1/billing/pricing/revisions.
//
// listPricingRevisions 处理 GET /operator/v1/billing/pricing/revisions。
func listPricingRevisions(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		page, err := ctx.Logic.ListPricingRevisions(r.Context(), actor, listQuery(r.URL.Query()))
		if err != nil {
			respondErr(w, err)
			return
		}
		items := make([]types.PricingRevision, len(page.Items))
		for i, rev := range page.Items {
			items[i] = renderPricingRevision(rev)
		}
		writeJSON(w, http.StatusOK, types.PricingRevisionListResponse{Items: items, NextCursor: page.NextCursor})
	})
}

// listPricePlans handles GET /operator/v1/billing/plans.
//
// listPricePlans 处理 GET /operator/v1/billing/plans。
func listPricePlans(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		view, err := ctx.Logic.ListPricePlans(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		items := make([]types.PricePlan, len(view.Plans))
		for i, plan := range view.Plans {
			items[i] = renderPricePlan(plan)
		}
		writeJSON(w, http.StatusOK, types.PricePlanListResponse{Revision: renderPricingRevision(view.Revision), Items: items})
	})
}

// savePricePlan handles POST /operator/v1/billing/plans (create) and
// PUT /operator/v1/billing/plans/:id.
//
// savePricePlan 处理 POST /operator/v1/billing/plans（create）与
// PUT /operator/v1/billing/plans/:id。
func savePricePlan(ctx *svc.ServiceContext, create bool) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		var req types.PricePlanRequest
		if !decode(w, r, &req) {
			return
		}
		in := logic.PricePlanInput{Name: req.Name, DefaultRatePPM: req.DefaultRatePPM, BaseRevision: req.BaseRevision}
		if req.Models != nil {
			in.Models = make(map[string]pricingconfig.PlanModel, len(req.Models))
			// Aliases are trimmed as PutPricingModel trims them; two entries
			// for one alias are refused rather than one silently winning.
			// 别名按 PutPricingModel 的方式去掉首尾空白；同一别名的两项会被拒绝，而不是
			// 悄悄地只保留其中一项。
			for _, m := range req.Models {
				alias := strings.TrimSpace(m.Model)
				if _, dup := in.Models[alias]; dup {
					respondErr(w, logic.ErrInvalidInput)
					return
				}
				in.Models[alias] = pricingconfig.PlanModel{RatePPM: m.RatePPM, Prices: m.Prices}
			}
		}
		id := ""
		if !create {
			id = pathvar.Vars(r)["id"]
		}
		result, err := ctx.Logic.SavePricePlan(r.Context(), actor, id, in, create)
		if err != nil {
			respondErr(w, err)
			return
		}
		status := http.StatusOK
		if create {
			status = http.StatusCreated
		}
		writeJSON(w, status, types.PricePlanResponse{Plan: renderPricePlan(result.Plan), Revision: renderPricingRevision(result.Revision)})
	})
}

// deletePricePlan handles DELETE /operator/v1/billing/plans/:id?base_revision=.
//
// deletePricePlan 处理 DELETE /operator/v1/billing/plans/:id?base_revision=。
func deletePricePlan(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		base, ok := baseRevisionParam(w, r.URL.Query().Get("base_revision"))
		if !ok {
			return
		}
		rev, err := ctx.Logic.DeletePricePlan(r.Context(), actor, pathvar.Vars(r)["id"], base)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, types.PricingRevisionResponse{Revision: renderPricingRevision(rev)})
	})
}

// setTenantPricePlan handles PUT /operator/v1/billing/tenants/:id/plan.
//
// setTenantPricePlan 处理 PUT /operator/v1/billing/tenants/:id/plan。
func setTenantPricePlan(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		var req types.TenantPricePlanRequest
		if !decode(w, r, &req) {
			return
		}
		planID := ""
		if req.PlanID != nil {
			planID = *req.PlanID
		}
		if err := ctx.Logic.SetTenantPricePlan(r.Context(), actor, pathvar.Vars(r)["id"], planID); err != nil {
			respondErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// baseRevisionParam reads a required positive base_revision query parameter,
// answering 400 when it is missing or malformed.
//
// baseRevisionParam 读取必填的正整数查询参数 base_revision；缺失或格式错误时答复 400。
func baseRevisionParam(w http.ResponseWriter, raw string) (int64, bool) {
	base, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || base <= 0 {
		writeError(w, http.StatusBadRequest, "base_revision must be a positive integer")
		return 0, false
	}
	return base, true
}

func renderPricingRevision(rev model.PricingRevision) types.PricingRevision {
	return types.PricingRevision{
		ID: rev.ID, EffectiveAt: rev.EffectiveAt, CreatedBy: rev.CreatedBy, Summary: rev.Summary, CreatedAt: rev.CreatedAt,
	}
}

func renderPricingConfig(view logic.PricingConfigView) types.PricingConfigResponse {
	doc := view.Document
	out := types.PricingConfigResponse{
		Revision:      renderPricingRevision(view.Revision),
		DefaultPrices: priceItems(doc.DefaultPrices),
		Models:        make([]types.PricingModel, 0, len(doc.Models)),
		Plans:         make([]types.PricePlan, 0, len(doc.Plans)),
		DefaultPlan:   doc.DefaultPlan,
	}
	for _, alias := range doc.SortedAliases() {
		cfg := doc.Models[alias]
		item := types.PricingModel{Model: alias, CatalogModel: cfg.CatalogModel, RatePPM: cfg.RatePPM}
		if len(cfg.Prices) > 0 {
			item.Prices = priceItems(cfg.Prices)
		}
		out.Models = append(out.Models, item)
	}
	for _, id := range doc.SortedPlanIDs() {
		out.Plans = append(out.Plans, renderPricePlan(logic.PricePlanView{ID: id, Plan: doc.Plans[id], Default: id == doc.DefaultPlan}))
	}
	return out
}

func renderPricePlan(view logic.PricePlanView) types.PricePlan {
	out := types.PricePlan{
		ID: view.ID, Name: view.Plan.Name, DefaultRatePPM: view.Plan.DefaultRatePPM, IsDefault: view.Default,
		Models: make([]types.PricePlanModel, 0, len(view.Plan.Models)), TenantCount: view.TenantCount,
	}
	aliases := make([]string, 0, len(view.Plan.Models))
	for alias := range view.Plan.Models {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		terms := view.Plan.Models[alias]
		item := types.PricePlanModel{Model: alias, RatePPM: terms.RatePPM}
		if len(terms.Prices) > 0 {
			item.Prices = priceItems(terms.Prices)
		}
		out.Models = append(out.Models, item)
	}
	return out
}

// priceItems returns a price list as a non-nil slice, so it renders as [].
//
// priceItems 把单价表转成非 nil 的切片，使其渲染为 []。
func priceItems(list pricing.PriceList) []pricing.PriceItem {
	return append([]pricing.PriceItem{}, list...)
}
