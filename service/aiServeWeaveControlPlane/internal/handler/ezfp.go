package handler

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

// MaxEzfpNotifyBytes bounds an 易支付 notification sent as a POST form; the
// documented fields are well under a kilobyte.
//
// MaxEzfpNotifyBytes 限制以 POST 表单发送的易支付通知大小；文档中的字段远小于 1 KB。
const MaxEzfpNotifyBytes = 16 << 10

// ezfpNotify handles GET and POST /billing/v1/ezfp/notify. 易支付 expects the
// plain text "success" for a notification it may stop sending; anything
// else makes it retry. A verified notification is acknowledged whatever its
// outcome, since a retry would only meet the same answer; a bad signature
// (401) or a failure to apply it (500) is not.
//
// ezfpNotify 处理 GET 与 POST /billing/v1/ezfp/notify。易支付要求以纯文本 "success"
// 确认可以停止发送的通知；其他任何答复都会让它重试。已验签的通知无论结果如何都予以确认，
// 因为重试只会得到相同的答复；签名错误（401）或应用失败（500）则不确认。
func ezfpNotify(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		params := r.URL.Query()
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, MaxEzfpNotifyBytes)
			if err := r.ParseForm(); err != nil {
				writeEzfpText(w, http.StatusBadRequest, "fail")
				return
			}
			params = mergeForm(params, r.PostForm)
		}
		_, err := ctx.Logic.HandleEzfpNotify(r.Context(), params)
		switch {
		case errors.Is(err, logic.ErrInvalidSignature):
			writeEzfpText(w, http.StatusUnauthorized, "fail")
		case errors.Is(err, logic.ErrNotFound):
			writeEzfpText(w, http.StatusNotFound, "fail")
		case err != nil:
			writeEzfpText(w, http.StatusInternalServerError, "fail")
		default:
			writeEzfpText(w, http.StatusOK, "success")
		}
	}
}

// mergeForm adds a POST form's values to the query's, keeping repeats so
// verification can refuse a parameter sent twice.
//
// mergeForm 把 POST 表单的值并入 query 的值，并保留重复项，使验签能拒绝发送了两次的参数。
func mergeForm(query, form url.Values) url.Values {
	out := url.Values{}
	for k, v := range query {
		out[k] = append(out[k], v...)
	}
	for k, v := range form {
		out[k] = append(out[k], v...)
	}
	return out
}

func writeEzfpText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// refreshEzfpOrder handles POST /admin/v1/billing/orders/:id/refresh.
//
// refreshEzfpOrder 处理 POST /admin/v1/billing/orders/:id/refresh。
func refreshEzfpOrder(ctx *svc.ServiceContext) http.HandlerFunc {
	return withActorHandler(func(w http.ResponseWriter, r *http.Request, actor logic.Actor) {
		order, err := ctx.Logic.RefreshEzfpOrder(r.Context(), actor, pathvar.Vars(r)["id"])
		if err != nil {
			respondErr(w, err)
			return
		}
		refundable, err := ctx.Logic.RefundableOrders(r.Context(), actor, []model.CheckoutOrder{order})
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, renderOrder(order, refundable[order.ID], false))
	})
}
