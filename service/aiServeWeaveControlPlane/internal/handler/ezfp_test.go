package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

// handlerEzfp verifies a notification by its "sig" parameter and answers
// queries as unpaid.
type handlerEzfp struct {
	billing.Ezfp
	last url.Values
}

func (h *handlerEzfp) CreateOrder(_ context.Context, req billing.EzfpOrderRequest) (billing.EzfpOrder, error) {
	return billing.EzfpOrder{TradeNo: "T" + req.OutTradeNo, PayURL: "https://pay.example/" + req.OutTradeNo}, nil
}

func (h *handlerEzfp) QueryOrder(_ context.Context, id string) (billing.EzfpPayment, error) {
	return billing.EzfpPayment{OutTradeNo: id, TradeNo: "T" + id, Status: billing.EzfpStatusUnpaid}, nil
}

func (h *handlerEzfp) ParseNotify(params url.Values) (billing.EzfpPayment, error) {
	h.last = params
	if params.Get("sig") != "ok" || len(params["out_trade_no"]) != 1 {
		return billing.EzfpPayment{}, billing.ErrInvalidSignature
	}
	return billing.EzfpPayment{
		TradeNo: "T" + params.Get("out_trade_no"), OutTradeNo: params.Get("out_trade_no"), Status: billing.EzfpStatusPaid,
		TradeStatus: billing.EzfpTradeSuccess, MoneyMinor: 6800, Param: params.Get("param"),
	}, nil
}

func TestEzfpHTTPContract(t *testing.T) {
	st := memstore.New()
	ctx := context.Background()
	ezfp := &handlerEzfp{}
	service := logic.New(st, runtime.NewSystemClock(), logic.WithBilling(logic.BillingConfig{Ezfp: &logic.EzfpConfig{
		Client: ezfp, Types: []string{"wxpay"}, NotifyURL: "https://cp.example/n", ReturnURL: "https://console.example/b",
	}}))
	services := &svc.ServiceContext{Logic: service}
	operator := logic.Actor{TenantID: model.PlatformScope, Role: model.RolePlatformOperator, UserID: "operator"}
	pkg, err := service.CreateCreditPackage(ctx, operator, logic.CreditPackageInput{
		Name: "CNY", Kind: model.PackageKindFixed, CreditsMicro: model.MicroPerCredit, PriceMinor: 6800, Currency: "CNY", Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	actor := logic.Actor{TenantID: "buyer", UserID: "owner", Role: model.RoleOwner}
	serve := func(h http.HandlerFunc, method, target, body string, vars map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		if vars != nil {
			r = pathvar.WithVars(r, vars)
		}
		r = r.WithContext(withActor(r.Context(), actor))
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}

	// The overview reports 易支付 and each package's providers.
	w := serve(billingOverview(services), http.MethodGet, "/admin/v1/billing", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"payment_methods":{"dodo":false,"alipay":false,"ezfp":true,"ezfp_types":["wxpay"]}`) ||
		!strings.Contains(w.Body.String(), `"dodo_available":false,"ezfp_available":true`) || strings.Contains(w.Body.String(), "dodo_product_id") {
		t.Fatal(w.Code, w.Body.String())
	}
	// A body with an unknown field is refused; provider and pay_type decode.
	if w := serve(createCheckout(services), http.MethodPost, "/admin/v1/billing/checkout", `{"package_id":"`+pkg.ID+`","provider":"ezfp","pay_type":"wxpay","x":1}`, nil); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := serve(createCheckout(services), http.MethodPost, "/admin/v1/billing/checkout", `{"package_id":"`+pkg.ID+`"}`, nil); w.Code != 404 {
		t.Fatal("a Dodo checkout without Dodo:", w.Code, w.Body.String())
	}
	w = serve(createCheckout(services), http.MethodPost, "/admin/v1/billing/checkout", `{"package_id":"`+pkg.ID+`","provider":"ezfp","pay_type":"wxpay"}`, nil)
	var checkout struct {
		OrderID     string `json:"order_id"`
		CheckoutURL string `json:"checkout_url"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &checkout) != nil || checkout.CheckoutURL != "https://pay.example/"+checkout.OrderID {
		t.Fatal(w.Code, w.Body.String())
	}

	// Refresh answers the order, with its provider and type.
	w = serve(refreshEzfpOrder(services), http.MethodPost, "/", "", map[string]string{"id": checkout.OrderID})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"provider":"ezfp","pay_type":"wxpay","status":"pending"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := serve(refreshEzfpOrder(services), http.MethodPost, "/", "", map[string]string{"id": "ord_missing"}); w.Code != 404 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r = pathvar.WithVars(r, map[string]string{"id": checkout.OrderID})
	unauth := httptest.NewRecorder()
	refreshEzfpOrder(services)(unauth, r)
	if unauth.Code != 401 {
		t.Fatal("refresh without a session:", unauth.Code)
	}

	notify := func(method, target, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		w := httptest.NewRecorder()
		ezfpNotify(services)(w, r)
		return w
	}
	textIs := func(w *httptest.ResponseRecorder, code int, body string) {
		t.Helper()
		if w.Code != code || w.Body.String() != body || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") ||
			w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%d %q %v; want %d %q", w.Code, w.Body.String(), w.Header(), code, body)
		}
	}
	textIs(notify(http.MethodGet, "/billing/v1/ezfp/notify?sig=bad&out_trade_no="+checkout.OrderID, ""), 401, "fail")
	textIs(notify(http.MethodGet, "/billing/v1/ezfp/notify?sig=ok&param=buyer&out_trade_no="+checkout.OrderID, ""), 200, "success")
	// A replay is acknowledged too, and a POST form is read like a query.
	textIs(notify(http.MethodPost, "/billing/v1/ezfp/notify", "sig=ok&param=buyer&out_trade_no="+checkout.OrderID), 200, "success")
	// A parameter repeated across the query and the form reaches verification
	// as a repeat, which refuses it.
	textIs(notify(http.MethodPost, "/billing/v1/ezfp/notify?out_trade_no=x", "sig=ok&out_trade_no="+checkout.OrderID), 401, "fail")
	textIs(notify(http.MethodPost, "/billing/v1/ezfp/notify", "sig=ok&big="+strings.Repeat("a", MaxEzfpNotifyBytes)), 400, "fail")
	w = serve(listBillingOrders(services, false), http.MethodGet, "/admin/v1/billing/orders", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"paid","payment_id":"T`+checkout.OrderID+`"`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestEzfpNotifyAbsentWithoutEzfp(t *testing.T) {
	services := &svc.ServiceContext{Logic: logic.New(memstore.New(), runtime.NewSystemClock())}
	w := httptest.NewRecorder()
	ezfpNotify(services)(w, httptest.NewRequest(http.MethodGet, "/billing/v1/ezfp/notify", nil))
	if w.Code != 404 || w.Body.String() != "fail" {
		t.Fatal(w.Code, w.Body.String())
	}
}
