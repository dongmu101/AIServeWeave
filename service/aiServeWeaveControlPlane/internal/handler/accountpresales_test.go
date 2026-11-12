package handler

import (
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zeromicro/go-zero/rest/pathvar"
)

func TestPresaleHTTPLifecycleAndSafeProjection(t *testing.T) {
	st := memstore.New()
	ctx := context.Background()
	provider := &accountHandlerProvider{}
	service := logic.New(st, runtime.NewSystemClock(), logic.WithBilling(logic.BillingConfig{Provider: provider}))
	services := &svc.ServiceContext{Logic: service}
	now := time.Now().UTC()
	for _, p := range []model.AccountPresale{
		{ID: "pre_on", Kind: model.AccountKindClaude, Name: "Claude presale", PriceMinor: 1500, Currency: "USD", DodoProductID: "pdt_presale_secret", Capacity: 1, Active: true, UpdatedAt: now},
		{ID: "pre_off", Kind: model.AccountKindCodex, Name: "Retired presale", PriceMinor: 1500, Currency: "USD", DodoProductID: "pdt_presale_secret", Capacity: 1, UpdatedAt: now},
	} {
		if err := st.SaveAccountPresale(ctx, p, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SaveAccountSale(ctx, model.AccountSale{ID: "acc_a", NodeID: "agent", RuntimeID: "claude-a", Kind: model.AccountKindClaude, Name: "Claude A", ExpiresAt: time.Unix(0, 0).UTC()}, true); err != nil {
		t.Fatal(err)
	}
	actor := logic.Actor{TenantID: "buyer", UserID: "owner", Role: model.RoleOwner}
	operator := logic.Actor{TenantID: model.PlatformScope, Role: model.RolePlatformOperator, UserID: "op"}
	if err := st.CreateTenant(ctx, &model.Tenant{ID: actor.TenantID, Name: "Buyer"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(ctx, &model.User{ID: actor.UserID, TenantID: actor.TenantID, Email: "owner@example.com", Role: model.RoleOwner}); err != nil {
		t.Fatal(err)
	}
	serve := func(h http.HandlerFunc, method, target, body string, vars map[string]string, a *logic.Actor) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		if vars != nil {
			r = pathvar.WithVars(r, vars)
		}
		if a != nil {
			r = r.WithContext(withActor(r.Context(), *a))
		}
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}

	tenantView := serve(listAccountPresales(services), http.MethodGet, "/admin/v1/billing/presales", "", nil, &actor)
	if tenantView.Code != 200 {
		t.Fatal(tenantView.Code, tenantView.Body.String())
	}
	for _, leak := range []string{"pdt_presale_secret", "capacity", "outstanding", "pre_off", "Retired"} {
		if strings.Contains(tenantView.Body.String(), leak) {
			t.Fatalf("tenant presales leak %q: %s", leak, tenantView.Body.String())
		}
	}
	if !strings.Contains(tenantView.Body.String(), `"available":true`) || !strings.Contains(tenantView.Body.String(), `"kind":"claude"`) {
		t.Fatal(tenantView.Body.String())
	}
	operatorView := serve(listAccountPresales(services), http.MethodGet, "/operator/v1/billing/presales", "", nil, &operator)
	if body := operatorView.Body.String(); !strings.Contains(body, `"dodo_product_id":"pdt_presale_secret"`) || !strings.Contains(body, `"capacity":1`) ||
		!strings.Contains(body, `"outstanding":0`) || !strings.Contains(body, "pre_off") {
		t.Fatal("operator presales", body)
	}

	checkout := func(body string) *httptest.ResponseRecorder {
		return serve(presaleCheckout(services), http.MethodPost, "/admin/v1/billing/presales/pre_on/checkout", body, map[string]string{"id": "pre_on"}, &actor)
	}
	if w := checkout(`{"expected_price_minor":1499,"currency":"USD"}`); w.Code != 409 {
		t.Fatal("stale price", w.Code)
	}
	w := checkout(`{"expected_price_minor":1500,"currency":"USD"}`)
	var opened struct {
		OrderID string `json:"order_id"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &opened) != nil || opened.OrderID == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := checkout(`{"expected_price_minor":1500,"currency":"USD"}`); w.Code != 409 {
		t.Fatal("capacity exceeded", w.Code)
	}
	if _, err := service.HandleBillingWebhook(ctx, nil, http.Header{}); err != nil {
		t.Fatal(err)
	}

	orders := func(query string) *httptest.ResponseRecorder {
		return serve(listBillingOrders(services, true), http.MethodGet, "/operator/v1/billing/orders"+query, "", nil, &operator)
	}
	if w := orders("?fulfillment=shipped"); w.Code != 400 {
		t.Fatal("unknown fulfillment filter", w.Code)
	}
	awaiting := orders("?fulfillment=awaiting")
	if awaiting.Code != 200 || !strings.Contains(awaiting.Body.String(), `"presale_id":"pre_on"`) || strings.Contains(awaiting.Body.String(), "account_expires_at") {
		t.Fatal("awaiting orders", awaiting.Code, awaiting.Body.String())
	}

	assign := func(a *logic.Actor, body string) *httptest.ResponseRecorder {
		return serve(assignPresaleOrder(services), http.MethodPost, "/operator/v1/billing/orders/"+opened.OrderID+"/assign", body, map[string]string{"id": opened.OrderID}, a)
	}
	if w := assign(nil, `{"account_id":"acc_a"}`); w.Code != 401 {
		t.Fatal("unauthenticated assignment", w.Code)
	}
	if w := assign(&actor, `{"account_id":"acc_a"}`); w.Code != 403 {
		t.Fatal("tenant assignment", w.Code)
	}
	if w := assign(&operator, `{"account_id":"acc_a","tenant_id":"x"}`); w.Code != 400 {
		t.Fatal("unknown field accepted", w.Code)
	}
	assigned := assign(&operator, `{"account_id":"acc_a"}`)
	var got struct {
		TenantID         string     `json:"tenant_id"`
		AccountID        string     `json:"account_id"`
		PresaleID        string     `json:"presale_id"`
		AccountExpiresAt *time.Time `json:"account_expires_at"`
	}
	if assigned.Code != 200 || json.Unmarshal(assigned.Body.Bytes(), &got) != nil || got.TenantID != "buyer" || got.AccountID != "acc_a" ||
		got.PresaleID != "pre_on" || got.AccountExpiresAt == nil {
		t.Fatal(assigned.Code, assigned.Body.String())
	}
	if w := assign(&operator, `{"account_id":"acc_a"}`); w.Code != 409 {
		t.Fatal("assigned twice", w.Code)
	}
	if after := orders("?fulfillment=awaiting"); !strings.Contains(after.Body.String(), `"items":[]`) {
		t.Fatal("assigned order still awaiting", after.Body.String())
	}

	catalog := serve(getCatalog(services), http.MethodGet, "/billing/v1/catalog", "", nil, nil)
	body := catalog.Body.String()
	if catalog.Code != 200 || !strings.Contains(body, `"presales":[{"id":"pre_on","kind":"claude","name":"Claude presale","price_minor":1500,"currency":"USD","available":true}]`) {
		t.Fatal("catalog presales", body)
	}
	if strings.Contains(body, "pdt_presale_secret") || strings.Contains(body, "pre_off") {
		t.Fatal("catalog leaks presale fields", body)
	}
}

func TestSaveAccountPresaleKeepsAlipayAmount(t *testing.T) {
	st := memstore.New()
	services := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock(), logic.WithBilling(logic.BillingConfig{Provider: &accountHandlerProvider{}}))}
	operator := logic.Actor{TenantID: model.PlatformScope, Role: model.RolePlatformOperator, UserID: "op"}
	serve := func(h http.HandlerFunc, method, body string, vars map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/operator/v1/billing/presales", strings.NewReader(body))
		if vars != nil {
			r = pathvar.WithVars(r, vars)
		}
		r = r.WithContext(withActor(r.Context(), operator))
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	created := serve(saveAccountPresale(services, true), http.MethodPost,
		`{"kind":"claude","name":"Alipay only","price_minor":250000,"currency":"CNY","dodo_product_id":"","capacity":10,"active":true,"alipay_amount":1}`, nil)
	var row struct {
		ID           string `json:"id"`
		AlipayAmount int64  `json:"alipay_amount"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &row) != nil || row.AlipayAmount != 1 {
		t.Fatal("create", created.Code, created.Body.String())
	}
	updated := serve(saveAccountPresale(services, false), http.MethodPut,
		`{"kind":"claude","name":"Alipay only","price_minor":250000,"currency":"CNY","dodo_product_id":"pdt_x","capacity":10,"active":true,"alipay_amount":25}`,
		map[string]string{"id": row.ID})
	if updated.Code != http.StatusOK || json.Unmarshal(updated.Body.Bytes(), &row) != nil || row.AlipayAmount != 25 {
		t.Fatal("update", updated.Code, updated.Body.String())
	}
	stored, err := st.ListAccountPresales(context.Background(), time.Now())
	if err != nil || len(stored) != 1 || stored[0].AlipayAmount != 25 {
		t.Fatal("stored", stored, err)
	}
}
