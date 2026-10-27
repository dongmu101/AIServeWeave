package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

func TestCreateRequestLogsAcceptsAValidEntryAndSkipsAnInvalidOne(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	body := `{"records":[
		{"request_id":"req_ok","tenant_id":"tnt_1","endpoint":"chat","status_code":200,"outcome":"ok","duration_ms":10,"created_at":"2026-09-11T08:00:00Z"},
		{"endpoint":"chat"}
	]}`
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/requestlogs", strings.NewReader(body))
	w := httptest.NewRecorder()

	createRequestLogs(ctx)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp types.CreateRequestLogsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Accepted != 1 {
		t.Fatalf("resp.Accepted = %d, want 1 (only the well-formed record)", resp.Accepted)
	}
}

func TestListRequestLogsTenantScopesToTheCallersTenant(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	now := time.Now()
	if err := st.CreateRequestLogs(t.Context(), []model.RequestLog{
		{ID: "req_mine", TenantID: "tnt_1", Endpoint: "chat", StatusCode: 200, Outcome: "ok", CreatedAt: now},
		{ID: "req_other", TenantID: "tnt_2", Endpoint: "chat", StatusCode: 200, Outcome: "ok", CreatedAt: now},
	}); err != nil {
		t.Fatalf("seeding CreateRequestLogs() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/requests", nil)
	req = req.WithContext(withActor(req.Context(), logic.Actor{TenantID: "tnt_1"}))
	w := httptest.NewRecorder()
	listRequestLogsTenant(ctx)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp types.RequestLogListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].RequestID != "req_mine" {
		t.Fatalf("resp.Items = %+v, want exactly req_mine (tnt_2's row must not leak in)", resp.Items)
	}
	if resp.Items[0].TenantID != "" {
		t.Errorf("resp.Items[0].TenantID = %q, want empty on the tenant-scoped endpoint", resp.Items[0].TenantID)
	}
}

func TestListRequestLogsTenantRejectsAMalformedSince(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/requests?since=not-a-time", nil)
	req = req.WithContext(withActor(req.Context(), logic.Actor{TenantID: "tnt_1"}))
	w := httptest.NewRecorder()
	listRequestLogsTenant(ctx)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a malformed since", w.Code)
	}
}

func TestListRequestLogsOperatorSearchesAcrossTenantsWhenNoneIsGiven(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	now := time.Now()
	if err := st.CreateRequestLogs(t.Context(), []model.RequestLog{
		{ID: "req_a", TenantID: "tnt_a", Endpoint: "chat", StatusCode: 200, Outcome: "ok", CreatedAt: now},
		{ID: "req_b", TenantID: "tnt_b", Endpoint: "chat", StatusCode: 200, Outcome: "ok", CreatedAt: now},
	}); err != nil {
		t.Fatalf("seeding CreateRequestLogs() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/operator/v1/requests", nil)
	w := httptest.NewRecorder()
	listRequestLogsOperator(ctx)(w, req)

	var resp types.RequestLogListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("resp.Items = %+v, want both tenants' rows with no tenant_id filter", resp.Items)
	}
	for _, item := range resp.Items {
		if item.TenantID == "" {
			t.Errorf("item %+v has an empty TenantID, want it populated on the operator endpoint", item)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/operator/v1/requests?tenant_id=tnt_a", nil)
	w = httptest.NewRecorder()
	listRequestLogsOperator(ctx)(w, req)
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].RequestID != "req_a" {
		t.Fatalf("resp.Items = %+v, want exactly req_a when tenant_id=tnt_a", resp.Items)
	}
}

func TestListRequestLogsOperatorRejectsAMalformedUntil(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	req := httptest.NewRequest(http.MethodGet, "/operator/v1/requests?until=not-a-time", nil)
	w := httptest.NewRecorder()
	listRequestLogsOperator(ctx)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a malformed until", w.Code)
	}
}
