package handler

import (
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"net/http"
	"time"
)

// renderAccountSale renders one account. The operator's view adds the
// holder and the Dodo product; the tenant's adds through which providers a
// month can be bought.
//
// renderAccountSale 呈现一个账号。运维视图附上持有者与 Dodo 产品；租户视图附上一个月可以
// 经哪些服务商购买。
func renderAccountSale(svcLogic *logic.Service, row model.AccountSale, actor logic.Actor) types.AccountSale {
	out := types.AccountSale{ID: row.ID, NodeID: row.NodeID, RuntimeID: row.RuntimeID, Kind: row.Kind, Name: row.Name, PriceMinor: row.PriceMinor, Currency: row.Currency, Active: row.Active, Owned: row.TenantID == actor.TenantID}
	if row.TenantID != "" {
		out.ExpiresAt = row.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	if actor.TenantID == model.PlatformScope {
		out.TenantID, out.DodoProductID = row.TenantID, row.DodoProductID
	} else {
		dodo, ezfp := svcLogic.FiatOffers(row.Currency, row.DodoProductID)
		out.DodoAvailable, out.EzfpAvailable = &dodo, &ezfp
	}
	return out
}

func listAccountSales(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		rows, err := ctx.Logic.ListAccountSales(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		items := make([]types.AccountSale, 0, len(rows))
		for _, row := range rows {
			items = append(items, renderAccountSale(ctx.Logic, row, actor))
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, struct {
			Items []types.AccountSale `json:"items"`
		}{items})
	})
}

func saveAccountSale(ctx *svc.ServiceContext, create bool) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		var req types.AccountSaleRequest
		if !decode(w, r, &req) {
			return
		}
		row, err := ctx.Logic.SaveAccountSale(r.Context(), actor, model.AccountSale{ID: pathvar.Vars(r)["id"], NodeID: req.NodeID, RuntimeID: req.RuntimeID, Kind: req.Kind, Name: req.Name, PriceMinor: req.PriceMinor, Currency: req.Currency, DodoProductID: req.DodoProductID, Active: req.Active}, create)
		if err != nil {
			respondErr(w, err)
			return
		}
		status := http.StatusOK
		if create {
			status = http.StatusCreated
		}
		writeJSON(w, status, renderAccountSale(ctx.Logic, row, actor))
	})
}

// accountCheckout handles POST /admin/v1/billing/accounts/:id/checkout.
//
// accountCheckout 处理 POST /admin/v1/billing/accounts/:id/checkout。
func accountCheckout(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		var req types.AccountCheckoutRequest
		if !decode(w, r, &req) {
			return
		}
		result, err := ctx.Logic.CreateAccountCheckout(r.Context(), actor, pathvar.Vars(r)["id"], req.ExpectedPriceMinor, req.Currency,
			logic.Payment{Provider: req.Provider, PayType: req.PayType})
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, types.CheckoutResponse{OrderID: result.OrderID, CheckoutURL: result.CheckoutURL})
	})
}
