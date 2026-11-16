package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/pricing"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// seedPricedRecords writes one priced record for tnt_1 and one for tnt_2.
// seedPricedRecords 为 tnt_1 与 tnt_2 各写入一条已计价的记录。
func seedPricedRecords(t *testing.T, st *memstore.Store) {
	t.Helper()
	details := `{"v":1,"m":[{"k":"input","q":1000,"x":400,"p":3000000,"c":1200},{"k":"input.cache_read","q":600,"x":600,"p":300000,"c":180},` +
		`{"k":"output","q":10,"x":10,"p":15000000,"c":150}],"rev":3,"plan":"plan_default","src":"catalog","ref":"claude-secret-ref","tier":0,"rate":1000000,"future":"hidden"}`
	charged := true
	now := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	if err := st.CreateUsageRecords(context.Background(), []model.UsageRecord{
		{ID: "req_1", TenantID: "tnt_1", Model: "claude-x", Endpoint: "chat", KeyID: "key_1", PromptTokens: 1000, CompletionTokens: 10, TotalTokens: 1010,
			CacheReadTokens: 600, CostMicro: 1530, BaseCostMicro: 1530, RatePPM: pricing.RateOne, PriceSource: model.PriceSourceCatalog, Charged: &charged, Details: &details, CreatedAt: now},
		{ID: "req_2", TenantID: "tnt_2", Model: "claude-x", Endpoint: "chat", PromptTokens: 5, TotalTokens: 5, CreatedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestListUsageRecordsScopesHidesRefAndShowsCostsWithBilling(t *testing.T) {
	st := memstore.New()
	seedPricedRecords(t, st)
	services := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock(), logic.WithBilling(logic.BillingConfig{Provider: &accountHandlerProvider{}}))}

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/usage/records?limit=10", nil)
	req = req.WithContext(withActor(req.Context(), logic.Actor{TenantID: "tnt_1"}))
	w := httptest.NewRecorder()
	listUsageRecordsTenant(services)(w, req)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	body := w.Body.String()
	var page types.UsageRecordListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].RequestID != "req_1" || page.Items[0].TenantID != "" {
		t.Fatalf("tenant page = %s, want only req_1 without a tenant id", body)
	}
	got := page.Items[0]
	if got.CostMicro == nil || *got.CostMicro != 1530 || got.PriceSource != model.PriceSourceCatalog || got.Charged == nil || !*got.Charged ||
		len(got.Meters) != 3 || got.Meters[0].PriceMicroPerM == nil || *got.Meters[0].PriceMicroPerM != 3_000_000 || got.Meters[0].Billed != 400 {
		t.Fatalf("record = %s", body)
	}
	for _, leak := range []string{"claude-secret-ref", "future", "hidden", "plan_default"} {
		if strings.Contains(body, leak) {
			t.Fatalf("tenant record leaks %q: %s", leak, body)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/operator/v1/usage/records?tenant_id=tnt_1", nil)
	w = httptest.NewRecorder()
	listUsageRecordsOperator(services)(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ref":"claude-secret-ref"`) || !strings.Contains(w.Body.String(), `"tenant_id":"tnt_1"`) {
		t.Fatalf("operator page = %d %s, want the ref and the tenant", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/v1/usage/records?since=yesterday", nil)
	req = req.WithContext(withActor(req.Context(), logic.Actor{TenantID: "tnt_1"}))
	w = httptest.NewRecorder()
	listUsageRecordsTenant(services)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed since = %d, want 400", w.Code)
	}
}

func TestUsageReadsOmitCostsWithBillingOff(t *testing.T) {
	st := memstore.New()
	seedPricedRecords(t, st)
	services := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/usage/records", nil)
	req = req.WithContext(withActor(req.Context(), logic.Actor{TenantID: "tnt_1"}))
	w := httptest.NewRecorder()
	listUsageRecordsTenant(services)(w, req)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `"cache_read_tokens":600`) || !strings.Contains(body, `"quantity":1000`) {
		t.Fatalf("records = %d %s", w.Code, body)
	}
	for _, field := range []string{"cost_micro", "price_micro_per_m", "price_source", "charged", "rate_ppm"} {
		if strings.Contains(body, field) {
			t.Fatalf("billing off still shows %q: %s", field, body)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/v1/usage/summary", nil)
	req = req.WithContext(withActor(req.Context(), logic.Actor{TenantID: "tnt_1"}))
	w = httptest.NewRecorder()
	summarizeUsageTenant(services)(w, req)
	if body := w.Body.String(); !strings.Contains(body, `"cache_read_tokens":600`) || strings.Contains(body, "cost_micro") {
		t.Fatalf("summary with billing off = %s", body)
	}

	on := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock(), logic.WithBilling(logic.BillingConfig{Provider: &accountHandlerProvider{}}))}
	w = httptest.NewRecorder()
	summarizeUsageOperator(on)(w, httptest.NewRequest(http.MethodGet, "/operator/v1/usage/summary?tenant_id=tnt_1", nil))
	if body := w.Body.String(); !strings.Contains(body, `"cost_micro":1530`) {
		t.Fatalf("summary with billing on = %s", body)
	}
}

func TestPricesEndpoints(t *testing.T) {
	st := memstore.New()
	ctx := context.Background()
	service := logic.New(st, runtime.NewSystemClock(), logic.WithBilling(logic.BillingConfig{Provider: &accountHandlerProvider{}}))
	services := &svc.ServiceContext{Logic: service}
	if err := st.CreateTenant(ctx, &model.Tenant{ID: "tnt_a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutModelPrice(ctx, model.ModelPrice{Model: "gpt-x", InputMicroPerMTok: 1_000_000, OutputMicroPerMTok: 2_000_000, UpdatedBy: "ops", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	publishTestRoutes(t, service, "gpt-x", "local")
	operator := logic.Actor{TenantID: model.PlatformScope, UserID: "operator", Role: model.RolePlatformOperator}

	req := httptest.NewRequest(http.MethodGet, "/operator/v1/billing/effective-prices", nil)
	req = req.WithContext(withActor(req.Context(), operator))
	w := httptest.NewRecorder()
	getEffectivePrices(services)(w, req)
	var prices types.EffectivePricesResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &prices) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(prices.Items) != 2 || len(prices.UnpricedAliases) != 1 || prices.UnpricedAliases[0] != "local" || prices.Items[0].ModelRatePPM == nil {
		t.Fatalf("effective prices = %s", w.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/operator/v1/billing/effective-prices?plan_id=plan_nope", nil)
	req = req.WithContext(withActor(req.Context(), operator))
	w = httptest.NewRecorder()
	getEffectivePrices(services)(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown plan = %d, want 404", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/v1/billing/prices", nil)
	req = req.WithContext(withActor(req.Context(), logic.Actor{TenantID: "tnt_a", UserID: "u", Role: model.RoleMember}))
	w = httptest.NewRecorder()
	getTenantPrices(services)(w, req)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `"model":"gpt-x"`) || !strings.Contains(body, `"sale_micro_per_m":2000000`) ||
		strings.Contains(body, "model_rate_ppm") || strings.Contains(body, `"ref"`) {
		t.Fatalf("tenant prices = %d %s", w.Code, body)
	}
}
