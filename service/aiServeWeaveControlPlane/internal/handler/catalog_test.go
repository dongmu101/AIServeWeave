package handler

import (
	"AIServeWeave/common/modelroute"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCatalogIsPublicAndSafe(t *testing.T) {
	st := memstore.New()
	ctx := context.Background()
	service := logic.New(st, runtime.NewSystemClock(), logic.WithBilling(logic.BillingConfig{Provider: &accountHandlerProvider{}}))
	services := &svc.ServiceContext{Logic: service}
	now := time.Now()
	for _, pkg := range []model.CreditPackage{
		{ID: "pkg_on", Name: "Starter", Kind: "fixed", DodoProductID: "pdt_secret", CreditsMicro: 5, PriceMinor: 500, Currency: "usd", Active: true},
		{ID: "pkg_off", Name: "Retired", Kind: "fixed", DodoProductID: "pdt_old", CreditsMicro: 1, PriceMinor: 100, Currency: "usd"},
	} {
		pkg := pkg
		if err := st.CreateCreditPackage(ctx, &pkg); err != nil {
			t.Fatal(err)
		}
	}
	// gpt-x is routed and priced (model_prices becomes its override); local
	// is routed but nothing prices it, so the catalog leaves it out.
	// gpt-x 有路由且有单价（model_prices 转成它的覆盖价）；local 有路由但没有任何单价，
	// 因此目录不列出它。
	if err := st.PutModelPrice(ctx, model.ModelPrice{Model: "gpt-x", InputMicroPerMTok: 1, OutputMicroPerMTok: 2, UpdatedBy: "ops_secret", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	publishTestRoutes(t, service, "gpt-x", "local-unpriced")
	sales := []model.AccountSale{
		{ID: "acc_on", NodeID: "node-secret", RuntimeID: "rt-secret", Name: "Claude A", PriceMinor: 1999, Currency: "USD", DodoProductID: "pdt_account_secret", Active: true},
		{ID: "acc_off", NodeID: "node-off", RuntimeID: "rt-off", Name: "Archived", PriceMinor: 1999, Currency: "USD", DodoProductID: "pdt_account_secret"},
		{ID: "acc_held", NodeID: "node-held", RuntimeID: "rt-held", Name: "Held", PriceMinor: 1999, Currency: "USD", DodoProductID: "pdt_account_secret", Active: true},
		// A legacy credit-priced offer has no fiat price and cannot be bought.
		{ID: "acc_legacy", NodeID: "node-legacy", RuntimeID: "rt-legacy", Name: "Legacy", Active: true},
	}
	for _, sale := range sales {
		if err := st.SaveAccountSale(ctx, sale, true); err != nil {
			t.Fatal(err)
		}
	}
	// acc_held is bought through a paid checkout.
	if err := st.CreateTenant(ctx, &model.Tenant{ID: "buyer", Name: "Buyer"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(ctx, &model.User{ID: "u", TenantID: "buyer", Email: "buyer@example.com", Role: model.RoleOwner}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateAccountCheckout(ctx, logic.Actor{TenantID: "buyer", UserID: "u", Role: model.RoleOwner}, "acc_held", 1999, "USD", logic.Payment{}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.HandleBillingWebhook(ctx, nil, http.Header{}); err != nil {
		t.Fatal(err)
	}

	// No actor in the context: the endpoint must answer without a session.
	w := httptest.NewRecorder()
	getCatalog(services)(w, httptest.NewRequest(http.MethodGet, "/billing/v1/catalog", nil))
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"Starter", "Claude A", "gpt-x", "acc_on", "pkg_on", `"price_minor":1999`, `"currency":"USD"`,
		`"input_micro_per_mtok":1`, `"output_micro_per_mtok":2`, `"meter":"input"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("catalog lacks %q: %s", want, body)
		}
	}
	for _, leak := range []string{"pdt_secret", "pdt_old", "Retired", "Archived", "Held", "Legacy", "pdt_account_secret", "node-", "rt-", "tenant_id", "buyer", "ops_secret", "updated_by", "local-unpriced", `"ref"`, `"source"`} {
		if strings.Contains(body, leak) {
			t.Fatalf("catalog leaks %q: %s", leak, body)
		}
	}
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Fatal(got)
	}
}

func TestCatalogDisabledIsNotFound(t *testing.T) {
	services := &svc.ServiceContext{Logic: logic.New(memstore.New(), runtime.NewSystemClock())}
	w := httptest.NewRecorder()
	getCatalog(services)(w, httptest.NewRequest(http.MethodGet, "/billing/v1/catalog", nil))
	if w.Code != http.StatusNotFound {
		t.Fatal(w.Code, w.Body.String())
	}
}

// Without payment billing nothing can be bought, so the catalog is empty.
func TestCatalogAccountsOnlyWithoutBilling(t *testing.T) {
	st := memstore.New()
	service := logic.New(st, runtime.NewSystemClock(), logic.WithAccountSales())
	if err := st.SaveAccountSale(context.Background(), model.AccountSale{ID: "acc_on", NodeID: "n", RuntimeID: "r", Name: "Claude A", PriceMinor: 1999, Currency: "USD", DodoProductID: "pdt", Active: true}, true); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	getCatalog(&svc.ServiceContext{Logic: service})(w, httptest.NewRequest(http.MethodGet, "/billing/v1/catalog", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"packages":[]`) || !strings.Contains(w.Body.String(), `"accounts":[]`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

// publishTestRoutes publishes one route per alias, each to a runtime model
// of the same name.
// publishTestRoutes 为每个别名发布一条路由，各自指向同名的运行时模型。
func publishTestRoutes(t *testing.T, service *logic.Service, aliases ...string) {
	t.Helper()
	routes := make([]modelroute.Route, len(aliases))
	for i, alias := range aliases {
		routes[i] = modelroute.Route{Model: alias, Targets: []modelroute.Target{{RuntimeModel: alias}}}
	}
	operator := logic.Actor{TenantID: model.PlatformScope, UserID: "operator", Role: model.RolePlatformOperator}
	current, err := service.CurrentRoutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PublishRoutes(context.Background(), operator, current.Revision, routes); err != nil {
		t.Fatal(err)
	}
}
