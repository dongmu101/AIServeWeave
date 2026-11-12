package handler

import (
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
	"net/http"
	"time"

	"github.com/zeromicro/go-zero/rest/pathvar"
)

// renderPresale renders a presale. A tenant sees only whether it can be
// bought; its product, capacity and waiting orders are the operator's.
//
// renderPresale 呈现一个预售。商户只看到能否购买；产品、名额与等待订单只给运维。
func renderPresale(svcLogic *logic.Service, row store.PresaleStock, actor logic.Actor) types.AccountPresale {
	out := types.AccountPresale{ID: row.ID, Kind: row.Kind, Name: row.Name, PriceMinor: row.PriceMinor, Currency: row.Currency,
		Active: row.Active, Available: logic.PresaleAvailable(row), AlipayAmount: row.AlipayAmount, AlipayAvailable: logic.PresaleAlipayAvailable(row),
		EzfpAvailable: svcLogic.PresaleEzfpAvailable(row)}
	if actor.TenantID == model.PlatformScope {
		capacity, outstanding := row.Capacity, row.Outstanding
		out.DodoProductID, out.Capacity, out.Outstanding = row.DodoProductID, &capacity, &outstanding
		out.UpdatedAt = row.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

// listAccountPresales handles GET /admin/v1/billing/presales and
// GET /operator/v1/billing/presales.
//
// listAccountPresales 处理 GET /admin/v1/billing/presales 与
// GET /operator/v1/billing/presales。
func listAccountPresales(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		rows, err := ctx.Logic.ListAccountPresales(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		items := make([]types.AccountPresale, 0, len(rows))
		for _, row := range rows {
			items = append(items, renderPresale(ctx.Logic, row, actor))
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, struct {
			Items []types.AccountPresale `json:"items"`
		}{items})
	})
}

// saveAccountPresale handles POST /operator/v1/billing/presales and
// PUT /operator/v1/billing/presales/:id.
//
// saveAccountPresale 处理 POST /operator/v1/billing/presales 与
// PUT /operator/v1/billing/presales/:id。
func saveAccountPresale(ctx *svc.ServiceContext, create bool) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		var req types.AccountPresaleRequest
		if !decode(w, r, &req) {
			return
		}
		row, err := ctx.Logic.SaveAccountPresale(r.Context(), actor, model.AccountPresale{
			ID: pathvar.Vars(r)["id"], Kind: req.Kind, Name: req.Name, PriceMinor: req.PriceMinor, Currency: req.Currency,
			DodoProductID: req.DodoProductID, Capacity: req.Capacity, Active: req.Active, AlipayAmount: req.AlipayAmount,
		}, create)
		if err != nil {
			respondErr(w, err)
			return
		}
		status := http.StatusOK
		if create {
			status = http.StatusCreated
		}
		writeJSON(w, status, renderPresale(ctx.Logic, row, actor))
	})
}

// presaleCheckout handles POST /admin/v1/billing/presales/:id/checkout.
//
// presaleCheckout 处理 POST /admin/v1/billing/presales/:id/checkout。
func presaleCheckout(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		var req types.AccountCheckoutRequest
		if !decode(w, r, &req) {
			return
		}
		result, err := ctx.Logic.CreatePresaleCheckout(r.Context(), actor, pathvar.Vars(r)["id"], req.ExpectedPriceMinor, req.Currency,
			logic.Payment{Provider: req.Provider, PayType: req.PayType})
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, types.CheckoutResponse{OrderID: result.OrderID, CheckoutURL: result.CheckoutURL})
	})
}

// presaleAlipayCheckout handles POST /admin/v1/billing/presales/:id/alipay. A
// redemption still pending answers 202: the click may be retried with the
// same action id, which resumes it.
//
// presaleAlipayCheckout 处理 POST /admin/v1/billing/presales/:id/alipay。仍待确认的
// 兑换回答 202：可以用同一 action id 重试这次点击，这会恢复它。
func presaleAlipayCheckout(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		var req types.PresaleAlipayRequest
		if !decode(w, r, &req) {
			return
		}
		got, err := ctx.Logic.CreatePresaleAlipayCheckout(r.Context(), actor, pathvar.Vars(r)["id"], req.ActionID, req.ExpectedAmount)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, redemptionStatus(got), renderAlipayRedemption(got))
	})
}

// assignPresaleOrder handles POST /operator/v1/billing/orders/:id/assign.
//
// assignPresaleOrder 处理 POST /operator/v1/billing/orders/:id/assign。
func assignPresaleOrder(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		var req types.PresaleAssignRequest
		if !decode(w, r, &req) {
			return
		}
		order, err := ctx.Logic.AssignPresaleOrder(r.Context(), actor, pathvar.Vars(r)["id"], req.AccountID)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, renderOrder(order, 0, true))
	})
}
