package handler

import (
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
	"errors"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"net/http"
	"time"
)

func renderAccountSale(row model.AccountSale, actor logic.Actor) types.AccountSale {
	out := types.AccountSale{ID: row.ID, NodeID: row.NodeID, RuntimeID: row.RuntimeID, Name: row.Name, PriceMicro: row.PriceMicro, Active: row.Active, Owned: row.TenantID == actor.TenantID}
	if row.TenantID != "" {
		out.ExpiresAt = row.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	if actor.TenantID == model.PlatformScope {
		out.TenantID = row.TenantID
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
			items = append(items, renderAccountSale(row, actor))
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
		row, err := ctx.Logic.SaveAccountSale(r.Context(), actor, model.AccountSale{ID: pathvar.Vars(r)["id"], NodeID: req.NodeID, RuntimeID: req.RuntimeID, Name: req.Name, PriceMicro: req.PriceMicro, Active: req.Active}, create)
		if err != nil {
			respondErr(w, err)
			return
		}
		status := http.StatusOK
		if create {
			status = http.StatusCreated
		}
		writeJSON(w, status, renderAccountSale(row, actor))
	})
}

func buyAccount(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		var req types.AccountPurchaseRequest
		if !decode(w, r, &req) {
			return
		}
		receipt, err := ctx.Logic.BuyAccount(r.Context(), actor, pathvar.Vars(r)["id"], req.RequestID, req.ExpectedPriceMicro)
		if errors.Is(err, store.ErrInsufficientCredit) {
			writeError(w, http.StatusPaymentRequired, "insufficient credit")
			return
		}
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, types.AccountPurchaseResponse{ID: receipt.ID, AccountID: receipt.AccountID, PriceMicro: receipt.PriceMicro, ExpiresAt: receipt.ExpiresAt.UTC().Format(time.RFC3339Nano)})
	})
}
