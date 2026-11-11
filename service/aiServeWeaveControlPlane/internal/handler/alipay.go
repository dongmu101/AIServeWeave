package handler

import (
	"net/http"

	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

func renderAlipayRedemption(r model.AlipayRedemption) types.AlipayRedemption {
	return types.AlipayRedemption{
		ID: r.ID, ActionID: r.ActionID, Amount: r.Amount, QuotaUnit: r.QuotaUnit, CreditsMicro: r.CreditsMicro,
		Status: r.Status, ReasonCode: r.ReasonCode, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// alipayOverview handles GET /admin/v1/billing/alipay.
//
// alipayOverview 处理 GET /admin/v1/billing/alipay。
func alipayOverview(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		got, err := ctx.Logic.AlipayOverview(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		out := types.AlipayOverviewResponse{
			CapabilityStatus: got.CapabilityStatus, ProductName: got.ProductName, BillingMode: got.BillingMode,
			QuotaUnit: got.QuotaUnit, Supported: got.Supported, CanPurchase: got.CanPurchase, CanRedeem: got.CanRedeem,
			Plans: make([]types.AlipayPlan, 0, len(got.Plans)), CreditsPerUnitMicro: got.CreditsPerUnitMicro, MaxAmount: got.MaxAmount,
		}
		for _, p := range got.Plans {
			out.Plans = append(out.Plans, types.AlipayPlan{
				SkuID: p.SkuID, Name: p.DisplayName, Price: p.Price, Currency: p.Currency, QuotaUnit: p.QuotaUnit, QuotaAmount: p.QuotaAmount,
			})
		}
		if got.AccessKnown {
			out.Access = &types.AlipayAccess{Total: got.Total, Used: got.Used, Remaining: got.Remaining}
		}
		if got.Pending != nil {
			pending := renderAlipayRedemption(*got.Pending)
			out.Pending = &pending
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, out)
	})
}

// alipayPurchase handles POST /admin/v1/billing/alipay/purchases.
//
// alipayPurchase 处理 POST /admin/v1/billing/alipay/purchases。
func alipayPurchase(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		var req types.AlipayPurchaseRequest
		if !decode(w, r, &req) {
			return
		}
		got, err := ctx.Logic.CreateAlipayPurchase(r.Context(), actor, req.ActionID, req.SkuID)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, types.AlipayPurchaseResponse{ID: got.ID, PurchaseURL: got.PurchaseURL, ExpireTime: got.ExpireTime})
	})
}

// alipayRedeem handles POST /admin/v1/billing/alipay/redemptions. A
// redemption still pending answers 202: the click may be retried with the
// same action id, which resumes it.
//
// alipayRedeem 处理 POST /admin/v1/billing/alipay/redemptions。仍待确认的兑换回答 202：
// 可以用同一 action id 重试这次点击，这会恢复它。
func alipayRedeem(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		var req types.AlipayRedeemRequest
		if !decode(w, r, &req) {
			return
		}
		got, err := ctx.Logic.RedeemAlipay(r.Context(), actor, req.ActionID, req.Amount)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, redemptionStatus(got), renderAlipayRedemption(got))
	})
}

// alipayRefresh handles POST /admin/v1/billing/alipay/redemptions/:id/refresh.
//
// alipayRefresh 处理 POST /admin/v1/billing/alipay/redemptions/:id/refresh。
func alipayRefresh(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		got, err := ctx.Logic.RefreshAlipayRedemption(r.Context(), actor, pathvar.Vars(r)["id"])
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, redemptionStatus(got), renderAlipayRedemption(got))
	})
}

func redemptionStatus(r model.AlipayRedemption) int {
	if r.Status == model.AlipayRedemptionPending {
		return http.StatusAccepted
	}
	return http.StatusOK
}

// listAlipayRedemptions handles GET /admin/v1/billing/alipay/redemptions.
//
// listAlipayRedemptions 处理 GET /admin/v1/billing/alipay/redemptions。
func listAlipayRedemptions(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		page, err := ctx.Logic.ListAlipayRedemptions(r.Context(), actor, listQuery(r.URL.Query()))
		if err != nil {
			respondErr(w, err)
			return
		}
		items := make([]types.AlipayRedemption, len(page.Items))
		for i, row := range page.Items {
			items[i] = renderAlipayRedemption(row)
		}
		writeJSON(w, http.StatusOK, types.AlipayRedemptionListResponse{Items: items, NextCursor: page.NextCursor})
	})
}
