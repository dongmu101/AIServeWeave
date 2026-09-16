package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

func TestCreateResponseTurnPersistsAndIsIdempotentOnRetry(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	body := `{"response_id":"resp_1","tenant_id":"tnt_1","model":"gpt-oss","messages":[{"Role":"user","Content":"hi"}]}`

	for i, want := range []int{http.StatusCreated, http.StatusCreated} {
		req := httptest.NewRequest(http.MethodPost, "/internal/v1/responses", strings.NewReader(body))
		w := httptest.NewRecorder()
		createResponseTurn(ctx)(w, req)
		if w.Code != want {
			t.Fatalf("attempt %d: status = %d, want %d, body=%s", i, w.Code, want, w.Body.String())
		}
		var resp types.ResponseTurnResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("attempt %d: unmarshal response: %v", i, err)
		}
		if resp.ResponseID != "resp_1" || resp.TenantID != "tnt_1" {
			t.Fatalf("attempt %d: resp = %+v, want response_id=resp_1 tenant_id=tnt_1", i, resp)
		}
	}
}

func TestGetResponseTurnRequiresTenantIDAndReturnsNotFoundForUnknownID(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/responses/resp_1", nil)
	req = pathvar.WithVars(req, map[string]string{"id": "resp_1"})
	w := httptest.NewRecorder()
	getResponseTurn(ctx)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing tenant_id: status = %d, want 400, body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/internal/v1/responses/resp_1?tenant_id=tnt_1", nil)
	req = pathvar.WithVars(req, map[string]string{"id": "resp_1"})
	w = httptest.NewRecorder()
	getResponseTurn(ctx)(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown id: status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
}
