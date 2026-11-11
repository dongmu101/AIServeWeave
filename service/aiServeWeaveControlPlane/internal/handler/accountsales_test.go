package handler

import (
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"context"
	"encoding/json"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type accountHandlerProvider struct {
	billing.Provider
	event store.BillingEvent
}

func (p *accountHandlerProvider) CreateCustomer(context.Context, string, string, string) (string, error) {
	return "cus", nil
}

func (p *accountHandlerProvider) CreateCheckout(_ context.Context, req billing.CheckoutRequest) (billing.Checkout, error) {
	p.event = store.BillingEvent{WebhookID: "wh", Kind: store.BillingEventPaymentSucceeded, OrderID: req.OrderID, TenantID: req.TenantID,
		PaymentID: "pay", AmountMinor: req.AmountMinor, Currency: req.Currency}
	return billing.Checkout{SessionID: "cks", URL: "https://checkout.example/" + req.OrderID}, nil
}

func (p *accountHandlerProvider) ParseWebhook([]byte, http.Header) (store.BillingEvent, error) {
	return p.event, nil
}

func TestAccountCheckoutHTTPAndSafeProjection(t *testing.T) {
	st := memstore.New()
	ctx := context.Background()
	provider := &accountHandlerProvider{}
	service := logic.New(st, runtime.NewSystemClock(), logic.WithBilling(logic.BillingConfig{Provider: provider}))
	services := &svc.ServiceContext{Logic: service}
	if err := st.SaveAccountSale(ctx, model.AccountSale{ID: "acc_a", NodeID: "agent", RuntimeID: "claude-a", Name: "Claude A", PriceMinor: 2000, Currency: "USD", DodoProductID: "pdt_secret", Active: true}, true); err != nil {
		t.Fatal(err)
	}
	actor := logic.Actor{TenantID: "buyer", UserID: "owner", Role: model.RoleOwner}
	// Registering the provider customer reads the buyer and its tenant.
	if err := st.CreateTenant(ctx, &model.Tenant{ID: actor.TenantID, Name: "Buyer"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(ctx, &model.User{ID: actor.UserID, TenantID: actor.TenantID, Email: "owner@example.com", Role: model.RoleOwner}); err != nil {
		t.Fatal(err)
	}
	call := func(body string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/admin/v1/billing/accounts/acc_a/checkout", strings.NewReader(body))
		r = pathvar.WithVars(r, map[string]string{"id": "acc_a"})
		if authenticated {
			r = r.WithContext(withActor(r.Context(), actor))
		}
		w := httptest.NewRecorder()
		accountCheckout(services)(w, r)
		return w
	}
	body := `{"expected_price_minor":2000,"currency":"USD"}`
	if w := call(body, false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call(`{"tenant_id":"another"}`, true); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := call(`{"expected_price_minor":1500,"currency":"USD"}`, true); w.Code != 409 {
		t.Fatal("stale price", w.Code)
	}
	w := call(body, true)
	var opened struct {
		OrderID     string `json:"order_id"`
		CheckoutURL string `json:"checkout_url"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &opened) != nil || !strings.HasPrefix(opened.CheckoutURL, "https://checkout.example/") {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := service.HandleBillingWebhook(ctx, nil, http.Header{}); err != nil {
		t.Fatal(err)
	}
	list := func(a logic.Actor) string {
		r := httptest.NewRequest(http.MethodGet, "/admin/v1/billing/accounts?tenant_id=other", nil)
		r = r.WithContext(withActor(r.Context(), a))
		w := httptest.NewRecorder()
		listAccountSales(services)(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	tenantView := list(actor)
	if strings.Contains(tenantView, "tenant_id") || strings.Contains(tenantView, "pdt_secret") {
		t.Fatal("tenant view leaked operator fields", tenantView)
	}
	var items struct {
		Items []struct {
			Owned      bool      `json:"owned"`
			PriceMinor int64     `json:"price_minor"`
			Currency   string    `json:"currency"`
			ExpiresAt  time.Time `json:"expires_at"`
		}
	}
	if err := json.Unmarshal([]byte(tenantView), &items); err != nil || len(items.Items) != 1 || !items.Items[0].Owned ||
		items.Items[0].ExpiresAt.IsZero() || items.Items[0].PriceMinor != 2000 || items.Items[0].Currency != "USD" {
		t.Fatal(tenantView, err)
	}
	if operatorView := list(logic.Actor{TenantID: model.PlatformScope, Role: model.RolePlatformOperator, UserID: "op"}); !strings.Contains(operatorView, `"dodo_product_id":"pdt_secret"`) || !strings.Contains(operatorView, `"tenant_id":"buyer"`) {
		t.Fatal("operator view", operatorView)
	}
	policy, err := service.AccountPolicy(ctx, actor.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	wire := renderVerification(logic.Verification{TenantID: actor.TenantID, Accounts: &policy})
	if !wire.Accounts.Only || len(wire.Accounts.Managed) != 1 {
		t.Fatal("verification dropped account policy", wire)
	}
}
