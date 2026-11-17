package handler

import (
	"net/http"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/pricecatalog"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// CatalogSyncTimeout bounds POST /operator/v1/billing/catalog/sync: the
// upstream fetch plus applying the result, which on a first import inserts a
// few tens of thousands of price rows.
//
// CatalogSyncTimeout 限制 POST /operator/v1/billing/catalog/sync：上游拉取加上采用结果；
// 首次导入时后者要插入几万行单价。
const CatalogSyncTimeout = pricecatalog.FetchTimeout + 30*time.Second

// listPriceCatalog handles GET /operator/v1/billing/catalog: a page of
// entries in name order, optionally narrowed by ?q= (a name prefix) and
// ?status=, with the sync state.
//
// listPriceCatalog 处理 GET /operator/v1/billing/catalog：按名称顺序返回一页条目，可用
// ?q=（名称前缀）与 ?status= 收窄，并附同步状态。
func listPriceCatalog(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		query := r.URL.Query()
		page, state, err := ctx.Logic.ListPriceCatalog(r.Context(), actor, listQuery(query),
			store.CatalogFilter{Prefix: query.Get("q"), Status: query.Get("status")})
		if err != nil {
			respondErr(w, err)
			return
		}
		items := make([]types.PriceCatalogEntry, len(page.Items))
		for i, e := range page.Items {
			items[i] = types.PriceCatalogEntry{
				Model: e.Model.Model, Provider: e.Model.Provider, Mode: e.Model.Mode, Status: e.Model.Status,
				Pending: e.Pending, Prices: renderCatalogRows(e.Prices), UpdatedAt: e.Model.UpdatedAt,
			}
		}
		writeJSON(w, http.StatusOK, types.PriceCatalogListResponse{Items: items, NextCursor: page.NextCursor, Sync: renderCatalogSync(state)})
	})
}

// syncPriceCatalog handles POST /operator/v1/billing/catalog/sync: one
// fetch of the upstream list, applied under the review rules. A failed
// fetch answers 503 and leaves the catalog as it was.
//
// syncPriceCatalog 处理 POST /operator/v1/billing/catalog/sync：拉取一次上游价目表并按
// 审核规则采用。拉取失败时答复 503，价目表保持不变。
func syncPriceCatalog(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		result, err := ctx.Logic.SyncPriceCatalog(r.Context(), &actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, types.PriceCatalogSyncResponse{
			Skipped: result.Skipped, Added: nonNilStrings(result.Added), Applied: renderCatalogChanges(result.Applied),
			Reviewed: renderCatalogChanges(result.Reviewed), Removed: nonNilStrings(result.Removed),
			Suppressed: result.Suppressed, Version: result.Version,
		})
	})
}

// listCatalogPending handles GET /operator/v1/billing/catalog/pending.
//
// listCatalogPending 处理 GET /operator/v1/billing/catalog/pending。
func listCatalogPending(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		page, err := ctx.Logic.ListCatalogPending(r.Context(), actor, listQuery(r.URL.Query()))
		if err != nil {
			respondErr(w, err)
			return
		}
		items := make([]types.PriceCatalogPendingItem, len(page.Items))
		for i, p := range page.Items {
			item := types.PriceCatalogPendingItem{
				Model: p.Pending.Model, Kind: p.Pending.Kind, Status: p.Status, MaxChangeBP: changeBP(p.Pending.MaxChangeBP),
				DetectedAt: p.Pending.DetectedAt, Current: renderCatalogRows(p.Current),
			}
			for _, price := range p.Proposed.Prices {
				item.Proposed = append(item.Proposed, types.PriceCatalogPrice{
					Meter: price.Meter, Variant: price.Variant, AboveInputTokens: price.AboveInputTokens,
					PriceMicroPerM: price.PriceMicroPerM, Derived: price.Derived,
				})
			}
			items[i] = item
		}
		writeJSON(w, http.StatusOK, types.PriceCatalogPendingListResponse{Items: items, NextCursor: page.NextCursor})
	})
}

// decideCatalogPending handles POST /operator/v1/billing/catalog/pending/approve
// and .../reject. The entry name travels in the body: upstream names contain
// "/".
//
// decideCatalogPending 处理 POST /operator/v1/billing/catalog/pending/approve 与
// .../reject。条目名称放在请求体里：上游名称含有 "/"。
func decideCatalogPending(ctx *svc.ServiceContext, approve bool) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		var req types.PriceCatalogDecisionRequest
		if !decode(w, r, &req) {
			return
		}
		decide := ctx.Logic.RejectCatalogPending
		if approve {
			decide = ctx.Logic.ApproveCatalogPending
		}
		decision, err := decide(r.Context(), actor, req.Model)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, types.PriceCatalogDecisionResponse{
			Model: decision.Model, Kind: decision.Kind, Version: decision.Version,
			Before: renderCatalogRows(decision.Before), After: renderCatalogRows(decision.After),
		})
	})
}

// renderCatalogRows converts stored prices for a response, never nil.
//
// renderCatalogRows 为响应转换存储的单价，结果从不为 nil。
func renderCatalogRows(rows []model.CatalogPrice) []types.PriceCatalogPrice {
	out := make([]types.PriceCatalogPrice, len(rows))
	for i, row := range rows {
		from := row.ValidFrom
		out[i] = types.PriceCatalogPrice{
			Meter: row.Meter, Variant: row.Variant, AboveInputTokens: row.AboveInputTokens,
			PriceMicroPerM: row.PriceMicroPerM, Derived: row.Derived, ValidFrom: &from,
		}
	}
	return out
}

// renderCatalogSync converts the sync state for a response.
//
// renderCatalogSync 为响应转换同步状态。
func renderCatalogSync(state model.CatalogSyncState) types.PriceCatalogSyncState {
	return types.PriceCatalogSyncState{
		Source: state.Source, EntryCount: state.EntryCount, Version: state.Version, SyncedAt: state.SyncedAt,
		CheckedAt: state.CheckedAt, LastError: state.LastError, LastErrorAt: state.LastErrorAt,
	}
}

// renderCatalogChanges converts change summaries for a response, never nil.
//
// renderCatalogChanges 为响应转换变动摘要，结果从不为 nil。
func renderCatalogChanges(changes []store.CatalogChange) []types.PriceCatalogChange {
	out := make([]types.PriceCatalogChange, len(changes))
	for i, c := range changes {
		out[i] = types.PriceCatalogChange{Model: c.Model, MaxChangeBP: changeBP(c.MaxChangeBP)}
	}
	return out
}

// changeBP maps pricecatalog.UnboundedChange to null.
//
// changeBP 把 pricecatalog.UnboundedChange 映射为 null。
func changeBP(bp int64) *int64 {
	if bp == pricecatalog.UnboundedChange {
		return nil
	}
	return &bp
}

// nonNilStrings returns s, or an empty slice for nil, so JSON says [].
//
// nonNilStrings 返回 s；s 为 nil 时返回空切片，使 JSON 输出 []。
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
