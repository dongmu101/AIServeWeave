package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/pricecatalog"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

func TestPricingConfigHTTP(t *testing.T) {
	st := memstore.New()
	ctx := context.Background()
	service := logic.New(st, runtime.NewSystemClock(), logic.WithBilling(logic.BillingConfig{Provider: &accountHandlerProvider{}}))
	services := &svc.ServiceContext{Logic: service}
	if err := st.CreateTenant(ctx, &model.Tenant{ID: "tnt_a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	// The reference model below must be a catalog entry.
	// 下面的参照模型必须是价目表中的条目。
	snap, err := pricecatalog.Parse([]byte(`{"openai/gpt-x":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":3e-6,"output_cost_per_token":1.5e-5}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyCatalogSnapshot(ctx, snap, store.CatalogApplyOptions{Source: "test", Embedded: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	operator := logic.Actor{TenantID: model.PlatformScope, UserID: "operator", Role: model.RolePlatformOperator}
	tenant := logic.Actor{TenantID: "tnt_a", UserID: "owner", Role: model.RoleOwner}
	call := func(h http.HandlerFunc, method, target, body string, vars map[string]string, actor *logic.Actor) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		if vars != nil {
			r = pathvar.WithVars(r, vars)
		}
		if actor != nil {
			r = r.WithContext(withActor(r.Context(), *actor))
		}
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	readConfig := func(w *httptest.ResponseRecorder) types.PricingConfigResponse {
		t.Helper()
		var out types.PricingConfigResponse
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		return out
	}

	if w := call(getPricingConfig(services), http.MethodGet, "/operator/v1/billing/pricing", "", nil, nil); w.Code != http.StatusUnauthorized {
		t.Fatal("no session", w.Code)
	}
	if w := call(getPricingConfig(services), http.MethodGet, "/operator/v1/billing/pricing", "", nil, &tenant); w.Code != http.StatusForbidden {
		t.Fatal("tenant session", w.Code)
	}
	config := readConfig(call(getPricingConfig(services), http.MethodGet, "/operator/v1/billing/pricing", "", nil, &operator))
	if config.Revision.ID != 1 || config.DefaultPlan != "plan_default" || len(config.Plans) != 1 || !config.Plans[0].IsDefault ||
		config.DefaultPrices == nil || config.Models == nil {
		t.Fatal("initial configuration", config)
	}
	base := strconv.FormatInt(config.Revision.ID, 10)

	// Unknown fields and a stale base are refused; an alias with "/" works.
	if w := call(putPricingModel(services), http.MethodPut, "/operator/v1/billing/pricing/models", `{"model":"a","rate":1,"base_revision":`+base+`}`, nil, &operator); w.Code != http.StatusBadRequest {
		t.Fatal("unknown field", w.Code)
	}
	if w := call(putPricingModel(services), http.MethodPut, "/operator/v1/billing/pricing/models", `{"model":"a","base_revision":99}`, nil, &operator); w.Code != http.StatusConflict {
		t.Fatal("stale base", w.Code)
	}
	config = readConfig(call(putPricingModel(services), http.MethodPut, "/operator/v1/billing/pricing/models",
		`{"model":"org/gpt-x","catalog_model":"openai/gpt-x","prices":[{"meter":"output","price_micro_per_m":15000000},{"meter":"input","price_micro_per_m":3000000}],"rate_ppm":1500000,"base_revision":`+base+`}`,
		nil, &operator))
	if len(config.Models) != 1 || config.Models[0].Model != "org/gpt-x" || config.Models[0].RatePPM != 1_500_000 ||
		len(config.Models[0].Prices) != 2 || config.Models[0].Prices[0].Meter != "input" {
		t.Fatal("put model", config.Models)
	}
	base = strconv.FormatInt(config.Revision.ID, 10)
	config = readConfig(call(putDefaultPrices(services), http.MethodPut, "/operator/v1/billing/pricing/default-prices",
		`{"prices":[{"meter":"input","price_micro_per_m":1},{"meter":"output","price_micro_per_m":2}],"base_revision":`+base+`}`, nil, &operator))
	if len(config.DefaultPrices) != 2 {
		t.Fatal("default prices", config.DefaultPrices)
	}
	base = strconv.FormatInt(config.Revision.ID, 10)

	// A model without an override renders prices as null.
	config = readConfig(call(putPricingModel(services), http.MethodPut, "/operator/v1/billing/pricing/models",
		`{"model":"plain","base_revision":`+base+`}`, nil, &operator))
	base = strconv.FormatInt(config.Revision.ID, 10)
	raw := call(getPricingConfig(services), http.MethodGet, "/operator/v1/billing/pricing", "", nil, &operator).Body.String()
	if !strings.Contains(raw, `{"model":"plain","catalog_model":"","prices":null,"rate_ppm":1000000}`) {
		t.Fatal("plain model rendering", raw)
	}

	if w := call(deletePricingModel(services), http.MethodDelete, "/operator/v1/billing/pricing/models?model=plain", "", nil, &operator); w.Code != http.StatusBadRequest {
		t.Fatal("delete without base", w.Code)
	}
	config = readConfig(call(deletePricingModel(services), http.MethodDelete, "/operator/v1/billing/pricing/models?model=plain&base_revision="+base, "", nil, &operator))
	if len(config.Models) != 1 {
		t.Fatal("delete model", config.Models)
	}
	base = strconv.FormatInt(config.Revision.ID, 10)

	// Plans.
	w := call(savePricePlan(services, true), http.MethodPost, "/operator/v1/billing/plans",
		`{"name":"VIP","default_rate_ppm":800000,"models":[{"model":"org/gpt-x","rate_ppm":900000},{"model":"b","prices":[{"meter":"input","price_micro_per_m":1}]}],"base_revision":`+base+`}`,
		nil, &operator)
	var saved types.PricePlanResponse
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &saved) != nil {
		t.Fatal("create plan", w.Code, w.Body.String())
	}
	vip := saved.Plan.ID
	if !strings.HasPrefix(vip, "plan_") || saved.Plan.IsDefault || len(saved.Plan.Models) != 2 || saved.Plan.Models[0].Model != "b" || saved.Plan.Models[0].RatePPM != nil {
		t.Fatal("created plan", saved.Plan)
	}
	base = strconv.FormatInt(saved.Revision.ID, 10)
	if w := call(savePricePlan(services, true), http.MethodPost, "/operator/v1/billing/plans",
		`{"name":"Dup","models":[{"model":"a","rate_ppm":1},{"model":"a","rate_ppm":2}],"base_revision":`+base+`}`, nil, &operator); w.Code != http.StatusBadRequest {
		t.Fatal("duplicate plan model", w.Code)
	}
	w = call(savePricePlan(services, false), http.MethodPut, "/operator/v1/billing/plans/"+vip, `{"name":"Gold","base_revision":`+base+`}`,
		map[string]string{"id": vip}, &operator)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.Plan.Name != "Gold" || saved.Plan.DefaultRatePPM != 800_000 {
		t.Fatal("update plan", w.Code, w.Body.String())
	}
	base = strconv.FormatInt(saved.Revision.ID, 10)

	if w := call(setTenantPricePlan(services), http.MethodPut, "/operator/v1/billing/tenants/tnt_a/plan", `{"plan_id":"`+vip+`"}`,
		map[string]string{"id": "tnt_a"}, &operator); w.Code != http.StatusNoContent {
		t.Fatal("assign", w.Code, w.Body.String())
	}
	if w := call(setTenantPricePlan(services), http.MethodPut, "/operator/v1/billing/tenants/tnt_missing/plan", `{"plan_id":null}`,
		map[string]string{"id": "tnt_missing"}, &operator); w.Code != http.StatusNotFound {
		t.Fatal("unknown tenant", w.Code)
	}
	w = call(listPricePlans(services), http.MethodGet, "/operator/v1/billing/plans", "", nil, &operator)
	var plans types.PricePlanListResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &plans) != nil || len(plans.Items) != 2 ||
		plans.Items[0].TenantCount != nil || plans.Items[1].TenantCount == nil || *plans.Items[1].TenantCount != 1 {
		t.Fatal("plan list", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"tenant_count":null`) {
		t.Fatal("the default plan must omit tenant_count", w.Body.String())
	}
	w = call(listBillingTenants(services), http.MethodGet, "/operator/v1/billing/tenants", "", nil, &operator)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"price_plan_id":"`+vip+`"`) {
		t.Fatal("tenant list plan", w.Code, w.Body.String())
	}
	if w := call(deletePricePlan(services), http.MethodDelete, "/operator/v1/billing/plans/"+vip+"?base_revision="+base, "",
		map[string]string{"id": vip}, &operator); w.Code != http.StatusConflict {
		t.Fatal("plan in use", w.Code)
	}
	if w := call(setTenantPricePlan(services), http.MethodPut, "/operator/v1/billing/tenants/tnt_a/plan", `{"plan_id":null}`,
		map[string]string{"id": "tnt_a"}, &operator); w.Code != http.StatusNoContent {
		t.Fatal("back to default", w.Code)
	}
	w = call(deletePricePlan(services), http.MethodDelete, "/operator/v1/billing/plans/"+vip+"?base_revision="+base, "",
		map[string]string{"id": vip}, &operator)
	var deleted types.PricingRevisionResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &deleted) != nil || deleted.Revision.Summary != "delete plan "+vip {
		t.Fatal("delete plan", w.Code, w.Body.String())
	}

	// History, newest first.
	w = call(listPricingRevisions(services), http.MethodGet, "/operator/v1/billing/pricing/revisions?limit=2", "", nil, &operator)
	var revs types.PricingRevisionListResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &revs) != nil || len(revs.Items) != 2 || revs.NextCursor == "" ||
		revs.Items[0].ID != deleted.Revision.ID || revs.Items[0].CreatedBy != "operator" {
		t.Fatal("revisions", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "document") {
		t.Fatal("revision list must not carry documents", w.Body.String())
	}
}

func TestPricingRoutesNeedBilling(t *testing.T) {
	services := &svc.ServiceContext{Logic: logic.New(memstore.New(), runtime.NewSystemClock())}
	operator := logic.Actor{TenantID: model.PlatformScope, UserID: "operator", Role: model.RolePlatformOperator}
	r := httptest.NewRequest(http.MethodGet, "/operator/v1/billing/pricing", nil).WithContext(withActor(context.Background(), operator))
	w := httptest.NewRecorder()
	getPricingConfig(services)(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatal("billing off", w.Code)
	}
}
