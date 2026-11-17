package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/pricecatalog"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// switchableSource serves the document currently set, or fails.
type switchableSource struct {
	data string
	fail bool
}

func (s *switchableSource) Fetch(context.Context) ([]byte, error) {
	if s.fail {
		return nil, context.DeadlineExceeded
	}
	return []byte(s.data), nil
}

func (s *switchableSource) Label() string { return "https://prices.example/list.json" }

func TestPriceCatalogHTTP(t *testing.T) {
	st := memstore.New()
	ctx := context.Background()
	remote := &switchableSource{data: `{
	  "anthropic/claude-x": {"mode": "chat", "litellm_provider": "anthropic", "input_cost_per_token": 3.3e-06, "output_cost_per_token": 1.5e-05},
	  "gpt-x": {"mode": "chat", "input_cost_per_token": 1e-06, "output_cost_per_token": 0}
	}`}
	service := logic.New(st, runtime.NewSystemClock(), logic.WithBilling(logic.BillingConfig{Provider: &accountHandlerProvider{}}),
		logic.WithPriceCatalog(logic.PriceCatalogConfig{
			Remote: remote, RefreshInterval: 0, MaxChangePercent: 30,
			Embedded: []byte(`{
			  "anthropic/claude-x": {"mode": "chat", "input_cost_per_token": 3e-06, "output_cost_per_token": 1.5e-05},
			  "gpt-x": {"mode": "chat", "input_cost_per_token": 1e-06, "output_cost_per_token": 2e-06}
			}`),
		}))
	if err := service.InitPriceCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	services := &svc.ServiceContext{Logic: service}
	operator := logic.Actor{TenantID: model.PlatformScope, Role: model.RolePlatformOperator, UserID: "op"}
	owner := logic.Actor{TenantID: "tnt", Role: model.RoleOwner, UserID: "owner"}
	serve := func(h http.HandlerFunc, method, target, body string, a logic.Actor) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r = r.WithContext(withActor(r.Context(), a))
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}

	if w := serve(listPriceCatalog(services), http.MethodGet, "/operator/v1/billing/catalog", "", owner); w.Code != http.StatusForbidden {
		t.Fatalf("a tenant owner: %d", w.Code)
	}
	w := serve(listPriceCatalog(services), http.MethodGet, "/operator/v1/billing/catalog?q=anthropic/&limit=10", "", operator)
	var list types.PriceCatalogListResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Items) != 1 || list.Sync.Source != pricecatalog.EmbeddedLabel || list.Sync.Version != 1 {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}
	if e := list.Items[0]; e.Model != "anthropic/claude-x" || e.Status != pricecatalog.StatusActive || len(e.Prices) != 2 || e.Prices[0].ValidFrom == nil {
		t.Fatalf("entry = %+v", e)
	}
	if w := serve(listPriceCatalog(services), http.MethodGet, "/operator/v1/billing/catalog?cursor=bogus", "", operator); w.Code != http.StatusBadRequest {
		t.Fatalf("bad cursor: %d", w.Code)
	}

	// Sync: claude-x +10% applies, gpt-x's output falling to zero waits.
	w = serve(syncPriceCatalog(services), http.MethodPost, "/operator/v1/billing/catalog/sync", "", operator)
	if w.Code != 200 {
		t.Fatalf("sync = %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`"applied":[{"model":"anthropic/claude-x","max_change_bp":1000}]`, `"reviewed":[{"model":"gpt-x","max_change_bp":10000}]`, `"added":[]`, `"version":2`} {
		if !strings.Contains(body, want) {
			t.Fatalf("sync body lacks %s: %s", want, body)
		}
	}
	w = serve(listCatalogPending(services), http.MethodGet, "/operator/v1/billing/catalog/pending", "", operator)
	var pending types.PriceCatalogPendingListResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &pending) != nil || len(pending.Items) != 1 {
		t.Fatalf("pending = %d %s", w.Code, w.Body.String())
	}
	if p := pending.Items[0]; p.Model != "gpt-x" || p.Kind != pricecatalog.PendingChange || p.MaxChangeBP == nil || *p.MaxChangeBP != 10_000 ||
		len(p.Current) != 2 || len(p.Proposed) != 2 {
		t.Fatalf("pending item = %+v", p)
	}

	if w := serve(decideCatalogPending(services, true), http.MethodPost, "/operator/v1/billing/catalog/pending/approve", `{"model":"gpt-x","extra":1}`, operator); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", w.Code)
	}
	if w := serve(decideCatalogPending(services, true), http.MethodPost, "/operator/v1/billing/catalog/pending/approve", `{"model":"nope"}`, operator); w.Code != http.StatusNotFound {
		t.Fatalf("nothing waits: %d", w.Code)
	}
	w = serve(decideCatalogPending(services, false), http.MethodPost, "/operator/v1/billing/catalog/pending/reject", `{"model":"gpt-x"}`, operator)
	var decision types.PriceCatalogDecisionResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &decision) != nil || decision.Kind != pricecatalog.PendingChange || len(decision.After) != 2 {
		t.Fatalf("reject = %d %s", w.Code, w.Body.String())
	}

	remote.fail = true
	if w := serve(syncPriceCatalog(services), http.MethodPost, "/operator/v1/billing/catalog/sync", "", operator); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed fetch: %d %s", w.Code, w.Body.String())
	}
	w = serve(listPriceCatalog(services), http.MethodGet, "/operator/v1/billing/catalog", "", operator)
	if !strings.Contains(w.Body.String(), `"last_error":`) {
		t.Fatalf("the failure is shown: %s", w.Body.String())
	}
}
