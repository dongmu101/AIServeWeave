package handler

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// MaxBillingWebhookBytes bounds a provider webhook body. Payment events are a
// few kilobytes; the bound is generous so a large product cart never fails
// verification for being cut short.
//
// MaxBillingWebhookBytes 限制服务商 webhook 的请求体大小。付款事件只有几 KB；这个
// 上限留得宽裕，让一个很大的购物车也不会因被截断而校验失败。
const MaxBillingWebhookBytes = 1 << 20

// BillingCallTimeout bounds the routes that call the payment provider
// (checkout and portal), which may make two provider calls in a row and so
// need more than the server's default timeout. It stays below the Console's
// default upstream timeout of 10 seconds, so a slow provider surfaces as this
// service's 503 rather than the Console's own timeout.
//
// BillingCallTimeout 限制调用支付服务商的路由（结账与门户）。它们可能连续发起两次服务商
// 调用，因此需要比服务器默认超时更长的时间。它低于 Console 默认 10 秒的上游超时，使缓慢
// 的服务商表现为本服务的 503，而不是 Console 自己的超时。
const BillingCallTimeout = 9 * time.Second

// billingWebhook handles POST /billing/v1/dodo/webhook. It reads the raw body
// itself rather than through decode: the signature covers the exact bytes,
// and the event carries fields this service does not model. A verified event
// is acknowledged with 200 whatever its outcome; only a bad signature (401),
// an event that arrived ahead of the one it follows (503) or a failure to
// apply it (500) is not, and the provider redelivers the last two.
//
// billingWebhook 处理 POST /billing/v1/dodo/webhook。它自己读取原始请求体，而不经过
// decode：签名覆盖的是精确的字节，且事件携带本服务不建模的字段。已验签的事件无论结果
// 如何都以 200 确认；只有签名错误（401）、早于其前置事件到达（503）或应用失败（500）
// 例外，服务商会重新投递后两者。
func billingWebhook(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBillingWebhookBytes))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "the webhook body is too large")
			return
		}
		result, err := ctx.Logic.HandleBillingWebhook(r.Context(), body, r.Header)
		switch {
		case errors.Is(err, logic.ErrInvalidSignature):
			writeError(w, http.StatusUnauthorized, "the webhook signature does not verify")
		case errors.Is(err, store.ErrBillingEventEarly):
			writeError(w, http.StatusServiceUnavailable, "the event arrived before the one it follows; retry later")
		case err != nil:
			respondErr(w, err)
		default:
			writeJSON(w, http.StatusOK, map[string]string{"outcome": result.Outcome})
		}
	}
}

// billingOverview handles GET /admin/v1/billing.
//
// billingOverview 处理 GET /admin/v1/billing。
func billingOverview(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		overview, err := ctx.Logic.BillingOverview(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		packages := make([]types.CreditPackage, len(overview.Packages))
		for i, pkg := range overview.Packages {
			packages[i] = renderCreditPackage(pkg, false)
		}
		writeJSON(w, http.StatusOK, types.BillingOverviewResponse{
			Mode: overview.Mode, BalanceMicro: overview.BalanceMicro, Blocked: overview.Blocked,
			HasCustomer: overview.HasCustomer, Packages: packages,
		})
	})
}

// createCheckout handles POST /admin/v1/billing/checkout.
//
// createCheckout 处理 POST /admin/v1/billing/checkout。
func createCheckout(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		var req types.CheckoutRequest
		if !decode(w, r, &req) {
			return
		}
		result, err := ctx.Logic.CreateCheckout(r.Context(), actor, req.PackageID, req.AmountMinor)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, types.CheckoutResponse{OrderID: result.OrderID, CheckoutURL: result.CheckoutURL})
	})
}

// billingPortal handles POST /admin/v1/billing/portal.
//
// billingPortal 处理 POST /admin/v1/billing/portal。
func billingPortal(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		link, err := ctx.Logic.BillingPortalLink(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, types.PortalResponse{URL: link})
	})
}

// listBillingLedger handles GET /admin/v1/billing/ledger and, with
// cross-tenant filters, GET /operator/v1/billing/ledger.
//
// listBillingLedger 处理 GET /admin/v1/billing/ledger，并以跨租户筛选处理
// GET /operator/v1/billing/ledger。
func listBillingLedger(ctx *svc.ServiceContext, operator bool) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		query := r.URL.Query()
		filter := store.CreditLedgerFilter{Kind: query.Get("kind")}
		if operator {
			filter.TenantID = query.Get("tenant_id")
		}
		page, err := ctx.Logic.ListBillingLedger(r.Context(), actor, listQuery(query), filter)
		if err != nil {
			respondErr(w, err)
			return
		}
		items := make([]types.LedgerEntry, len(page.Items))
		for i, e := range page.Items {
			items[i] = types.LedgerEntry{
				ID: e.ID, Kind: e.Kind, AmountMicro: e.AmountMicro, BalanceAfterMicro: e.BalanceAfterMicro,
				Ref: e.Ref, CreatedAt: e.CreatedAt,
			}
			if operator {
				items[i].TenantID = e.TenantID
			}
		}
		writeJSON(w, http.StatusOK, types.LedgerListResponse{Items: items, NextCursor: page.NextCursor})
	})
}

// listBillingOrders handles GET /admin/v1/billing/orders and, with
// cross-tenant filters, GET /operator/v1/billing/orders.
//
// listBillingOrders 处理 GET /admin/v1/billing/orders，并以跨租户筛选处理
// GET /operator/v1/billing/orders。
func listBillingOrders(ctx *svc.ServiceContext, operator bool) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		query := r.URL.Query()
		filter := store.CheckoutOrderFilter{Status: query.Get("status")}
		if operator {
			filter.TenantID = query.Get("tenant_id")
		}
		page, err := ctx.Logic.ListBillingOrders(r.Context(), actor, listQuery(query), filter)
		if err != nil {
			respondErr(w, err)
			return
		}
		items := make([]types.CheckoutOrder, len(page.Items))
		for i, o := range page.Items {
			items[i] = types.CheckoutOrder{
				ID: o.ID, PackageID: o.PackageID, AmountMinor: o.AmountMinor, Currency: o.Currency,
				CreditsMicro: o.CreditsMicro, RefundedMinor: o.RefundedMinor, Status: o.Status,
				CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
			}
			if o.DodoPaymentID != nil {
				items[i].PaymentID = *o.DodoPaymentID
			}
			if operator {
				items[i].TenantID = o.TenantID
			}
		}
		writeJSON(w, http.StatusOK, types.OrderListResponse{Items: items, NextCursor: page.NextCursor})
	})
}

// listCreditPackages handles GET /operator/v1/billing/packages.
//
// listCreditPackages 处理 GET /operator/v1/billing/packages。
func listCreditPackages(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		packages, err := ctx.Logic.ListCreditPackagesAll(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		items := make([]types.CreditPackage, len(packages))
		for i, pkg := range packages {
			items[i] = renderCreditPackage(pkg, true)
		}
		writeJSON(w, http.StatusOK, types.CreditPackageListResponse{Items: items})
	})
}

// saveCreditPackage handles POST /operator/v1/billing/packages (create) and
// PUT /operator/v1/billing/packages/:id (replace).
//
// saveCreditPackage 处理 POST /operator/v1/billing/packages（创建）与
// PUT /operator/v1/billing/packages/:id（替换）。
func saveCreditPackage(ctx *svc.ServiceContext, create bool) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		var req types.CreditPackageRequest
		if !decode(w, r, &req) {
			return
		}
		in := logic.CreditPackageInput{
			Name: req.Name, Kind: req.Kind, DodoProductID: req.DodoProductID, CreditsMicro: req.CreditsMicro,
			PriceMinor: req.PriceMinor, Currency: req.Currency, CreditsPerMinorUnitMicro: req.CreditsPerMinorUnitMicro,
			MinAmountMinor: req.MinAmountMinor, MaxAmountMinor: req.MaxAmountMinor, Active: req.Active, SortOrder: req.SortOrder,
		}
		var (
			pkg    model.CreditPackage
			err    error
			status = http.StatusOK
		)
		if create {
			pkg, err = ctx.Logic.CreateCreditPackage(r.Context(), actor, in)
			status = http.StatusCreated
		} else {
			pkg, err = ctx.Logic.UpdateCreditPackage(r.Context(), actor, pathvar.Vars(r)["id"], in)
		}
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, status, renderCreditPackage(pkg, true))
	})
}

// listModelPrices handles GET /operator/v1/billing/model-prices.
//
// listModelPrices 处理 GET /operator/v1/billing/model-prices。
func listModelPrices(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		prices, err := ctx.Logic.ListModelPrices(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		items := make([]types.ModelPrice, len(prices))
		for i, p := range prices {
			items[i] = renderModelPrice(p)
		}
		writeJSON(w, http.StatusOK, types.ModelPriceListResponse{Items: items})
	})
}

// putModelPrice handles PUT /operator/v1/billing/model-prices. The alias
// travels in the body rather than the path because aliases may contain "/";
// the alias "*" sets the default price.
//
// putModelPrice 处理 PUT /operator/v1/billing/model-prices。别名放在请求体而不是
// 路径里，因为别名可能包含 "/"；别名 "*" 设置默认单价。
func putModelPrice(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		var req types.ModelPriceRequest
		if !decode(w, r, &req) {
			return
		}
		price, err := ctx.Logic.PutModelPrice(r.Context(), actor, req.Model, req.InputMicroPerMTok, req.OutputMicroPerMTok)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, renderModelPrice(price))
	})
}

// deleteModelPrice handles DELETE /operator/v1/billing/model-prices?model=,
// with the alias in the query for the reason putModelPrice gives.
//
// deleteModelPrice 处理 DELETE /operator/v1/billing/model-prices?model=，别名放在
// 查询参数里，理由见 putModelPrice。
func deleteModelPrice(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		if err := ctx.Logic.DeleteModelPrice(r.Context(), actor, r.URL.Query().Get("model")); err != nil {
			respondErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// listBillingTenants handles GET /operator/v1/billing/tenants, accepting the
// same status and q filters as GET /operator/v1/tenants.
//
// listBillingTenants 处理 GET /operator/v1/billing/tenants，接受与
// GET /operator/v1/tenants 相同的 status 与 q 筛选。
func listBillingTenants(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		query := r.URL.Query()
		page, err := ctx.Logic.ListBillingTenants(r.Context(), actor, listQuery(query), store.TenantFilter{
			Status: query.Get("status"), Query: query.Get("q"),
		})
		if err != nil {
			respondErr(w, err)
			return
		}
		items := make([]types.BillingTenant, len(page.Items))
		for i, v := range page.Items {
			items[i] = types.BillingTenant{
				ID: v.Tenant.ID, Name: v.Tenant.Name, Status: v.Tenant.Status, Mode: v.Billing.Mode,
				BalanceMicro: v.Billing.BalanceMicro, Blocked: v.Billing.Blocked(), CreatedAt: v.Tenant.CreatedAt,
			}
		}
		writeJSON(w, http.StatusOK, types.BillingTenantListResponse{Items: items, NextCursor: page.NextCursor})
	})
}

// setBillingMode handles PUT /operator/v1/billing/tenants/:id/mode.
//
// setBillingMode 处理 PUT /operator/v1/billing/tenants/:id/mode。
func setBillingMode(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		var req types.BillingModeRequest
		if !decode(w, r, &req) {
			return
		}
		if err := ctx.Logic.SetTenantBillingMode(r.Context(), actor, pathvar.Vars(r)["id"], req.Mode); err != nil {
			respondErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// adjustCredit handles POST /operator/v1/billing/tenants/:id/adjust.
//
// adjustCredit 处理 POST /operator/v1/billing/tenants/:id/adjust。
func adjustCredit(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		var req types.CreditAdjustRequest
		if !decode(w, r, &req) {
			return
		}
		entry, err := ctx.Logic.AdjustTenantCredit(r.Context(), actor, pathvar.Vars(r)["id"], req.AmountMicro, req.Reason)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, types.LedgerEntry{
			ID: entry.ID, TenantID: entry.TenantID, Kind: entry.Kind, AmountMicro: entry.AmountMicro,
			BalanceAfterMicro: entry.BalanceAfterMicro, Ref: entry.Ref, CreatedAt: entry.CreatedAt,
		})
	})
}

// withActorHandler resolves the authenticated actor once for a handler that
// always needs one.
//
// withActorHandler 为总是需要 actor 的 handler 统一解析已认证的调用方。
func withActorHandler(next func(http.ResponseWriter, *http.Request, logic.Actor)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r, actor)
	}
}

// renderCreditPackage writes a package. The provider product id is an
// operator detail and is shown to operators only.
//
// renderCreditPackage 写出一个充值包。服务商产品 id 属于运维细节，只展示给运维。
func renderCreditPackage(pkg model.CreditPackage, operator bool) types.CreditPackage {
	out := types.CreditPackage{
		ID: pkg.ID, Name: pkg.Name, Kind: pkg.Kind, CreditsMicro: pkg.CreditsMicro, PriceMinor: pkg.PriceMinor,
		Currency: strings.ToUpper(pkg.Currency), CreditsPerMinorUnitMicro: pkg.CreditsPerMinorUnitMicro,
		MinAmountMinor: pkg.MinAmountMinor, MaxAmountMinor: pkg.MaxAmountMinor, Active: pkg.Active,
		SortOrder: pkg.SortOrder, CreatedAt: pkg.CreatedAt, UpdatedAt: pkg.UpdatedAt,
	}
	if operator {
		out.DodoProductID = pkg.DodoProductID
	}
	return out
}

func renderModelPrice(p model.ModelPrice) types.ModelPrice {
	return types.ModelPrice{
		Model: p.Model, InputMicroPerMTok: p.InputMicroPerMTok, OutputMicroPerMTok: p.OutputMicroPerMTok,
		UpdatedBy: p.UpdatedBy, UpdatedAt: p.UpdatedAt,
	}
}
