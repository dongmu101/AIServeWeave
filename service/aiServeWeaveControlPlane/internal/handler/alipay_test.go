package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

// handlerAlipay consumes whatever it is asked, except while unknown is set.
type handlerAlipay struct {
	mu      sync.Mutex
	unknown bool
	users   []string
}

func (h *handlerAlipay) Capabilities(context.Context) (billing.AlipayCapabilities, error) {
	return billing.AlipayCapabilities{ProviderConfigured: true, PurchaseEntry: true, EntitlementQuery: true, Consumption: true}, nil
}

func (h *handlerAlipay) Product(context.Context) (billing.AlipayProduct, error) {
	return billing.AlipayProduct{CapabilityStatus: billing.AlipayCapabilityEnabled, BillingMode: billing.AlipayModeQuantity, QuotaUnit: billing.AlipayUnitPoint,
		Options: []billing.AlipayOption{{SkuID: "sku_1", DisplayName: "100 积分", Price: "9.90", Currency: "CNY", QuotaUnit: billing.AlipayUnitPoint, QuotaAmount: 100}}}, nil
}

func (h *handlerAlipay) Access(context.Context, string, int64) (billing.AlipayAccess, error) {
	return billing.AlipayAccess{QuotaUnit: billing.AlipayUnitPoint, Total: 100, Remaining: 60, Used: 40}, nil
}

func (h *handlerAlipay) Purchase(context.Context, billing.AlipayPurchaseRequest) (billing.AlipayPurchase, error) {
	return billing.AlipayPurchase{Status: billing.AlipayPurchaseReady, URL: "https://openapi.alipay.example/entry"}, nil
}

func (h *handlerAlipay) Consume(_ context.Context, req billing.AlipayConsumeRequest) (billing.AlipayConsumption, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.users = append(h.users, req.UserID)
	if h.unknown {
		return billing.AlipayConsumption{Status: billing.AlipayConsumeUnknown}, nil
	}
	return billing.AlipayConsumption{Status: billing.AlipayConsumeSucceeded, Confirmed: true, IdempotencyKey: req.IdempotencyKey,
		QuotaUnit: billing.AlipayUnitPoint, Consumed: req.Amount}, nil
}

func (h *handlerAlipay) Consumption(context.Context, string, string) (billing.AlipayConsumption, error) {
	return billing.AlipayConsumption{Status: billing.AlipayConsumeUnknown}, nil
}

func TestAlipayHTTPContract(t *testing.T) {
	st := memstore.New()
	fake := &handlerAlipay{}
	service := logic.New(st, runtime.NewSystemClock(), logic.WithBilling(logic.BillingConfig{
		Alipay: &logic.AlipayConfig{Client: fake, CreditsPerUnitMicro: 1_000_000},
	}))
	services := &svc.ServiceContext{Logic: service}
	actor := logic.Actor{TenantID: "tnt_buyer", UserID: "usr_owner", Role: model.RoleOwner}
	serve := func(h http.HandlerFunc, method, body string, vars map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/admin/v1/billing/alipay", strings.NewReader(body))
		if vars != nil {
			r = pathvar.WithVars(r, vars)
		}
		r = r.WithContext(withActor(r.Context(), actor))
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}

	if w := serve(billingOverview(services), http.MethodGet, "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"payment_methods":{"dodo":false,"alipay":true,"ezfp":false,"ezfp_types":[]}`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w := serve(alipayOverview(services), http.MethodGet, "", nil)
	var overview map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &overview); err != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if overview["supported"] != true || overview["can_redeem"] != true || overview["pending"] != nil ||
		overview["access"].(map[string]any)["remaining"] != float64(60) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Body.String())
	}

	// The user, rate and credits come from the server: a body naming them is refused.
	for _, body := range []string{
		`{"action_id":"action-http-1","amount":3,"user_id":"tnt_other"}`,
		`{"action_id":"action-http-1","amount":3,"credits_micro":999999999}`,
	} {
		if w := serve(alipayRedeem(services), http.MethodPost, body, nil); w.Code != 400 {
			t.Fatal("accepted", body, w.Code)
		}
	}
	if w := serve(alipayRedeem(services), http.MethodPost, `{"action_id":"action-http-1","amount":3}`, nil); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"status":"succeeded"`) || !strings.Contains(w.Body.String(), `"credits_micro":3000000`) {
		t.Fatal(w.Code, w.Body.String())
	}
	fake.unknown = true
	w = serve(alipayRedeem(services), http.MethodPost, `{"action_id":"action-http-2","amount":1}`, nil)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"status":"pending"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	var pending struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &pending)
	if w := serve(alipayRefresh(services), http.MethodPost, "", map[string]string{"id": pending.ID}); w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := serve(alipayRedeem(services), http.MethodPost, `{"action_id":"action-http-3","amount":1}`, nil); w.Code != 409 {
		t.Fatal("a second redemption while one is pending", w.Code)
	}
	if w := serve(listAlipayRedemptions(services), http.MethodGet, "", nil); w.Code != 200 || strings.Count(w.Body.String(), `"id":"apr_`) != 2 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := serve(alipayPurchase(services), http.MethodPost, `{"action_id":"action-http-4","sku_id":"sku_1"}`, nil); w.Code != 201 ||
		!strings.Contains(w.Body.String(), `"purchase_url":"https://openapi.alipay.example/entry"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, user := range fake.users {
		if user != actor.TenantID {
			t.Fatal("consumed for", user)
		}
	}
}
