package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"

	"github.com/zeromicro/go-zero/rest/pathvar"
)

// platformActor is one Actor that passes requirePlatformActor, for tests that
// only care about the handler layer and not about authorization itself —
// alertrules_test.go in the logic package already covers rejecting a
// non-platform actor.
//
// platformActor 是一个能通过 requirePlatformActor 的 Actor，供只关心 handler
// 这一层、不关心授权本身的测试使用——拒绝非平台 actor 这件事已经由 logic 包下
// 的 alertrules_test.go 覆盖过了。
func platformActor() logic.Actor {
	return logic.Actor{UserID: model.NewID(model.PrefixUser), TenantID: model.PlatformScope, Role: model.RolePlatformOperator}
}

// validAlertRuleRequestBody returns one AlertRuleRequest JSON body that
// passes every validation check, for tests that mutate a single field to
// make it invalid.
//
// validAlertRuleRequestBody 返回一份能通过全部校验的 AlertRuleRequest JSON
// 请求体，供测试改动单个字段使其非法时使用。
func validAlertRuleRequestBody() string {
	return `{"name":"high error rate","metric":"success_rate","operator":"lt","threshold":0.95,"consecutive_buckets":3,"webhook_url":"https://example.com/hook","enabled":true}`
}

func newAlertRuleTestContext() *svc.ServiceContext {
	return &svc.ServiceContext{Logic: logic.New(memstore.New(), runtime.NewSystemClock())}
}

func TestCreateAlertRuleRejectsAnInvalidMetricOrOperator(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown metric", body: `{"name":"r","metric":"not_a_metric","operator":"lt","threshold":1,"consecutive_buckets":3,"enabled":true}`},
		{name: "unknown operator", body: `{"name":"r","metric":"success_rate","operator":"!=","threshold":1,"consecutive_buckets":3,"enabled":true}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newAlertRuleTestContext()
			req := httptest.NewRequest(http.MethodPost, "/operator/v1/alert-rules", strings.NewReader(tt.body))
			req = req.WithContext(withActor(req.Context(), platformActor()))
			w := httptest.NewRecorder()

			createAlertRule(ctx)(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d (body=%s), got body=%s", w.Code, http.StatusBadRequest, tt.body, w.Body.String())
			}
		})
	}
}

func TestCreateAlertRuleSucceeds(t *testing.T) {
	ctx := newAlertRuleTestContext()
	req := httptest.NewRequest(http.MethodPost, "/operator/v1/alert-rules", strings.NewReader(validAlertRuleRequestBody()))
	req = req.WithContext(withActor(req.Context(), platformActor()))
	w := httptest.NewRecorder()

	createAlertRule(ctx)(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusCreated, w.Body.String())
	}
	var resp types.AlertRuleResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.ID == "" || resp.Name != "high error rate" || resp.Metric != model.MetricSuccessRate {
		t.Errorf("resp = %+v, want a populated rule matching the request body", resp)
	}
}

func TestListAlertRulesFiltersByEnabled(t *testing.T) {
	ctx := newAlertRuleTestContext()
	actor := platformActor()

	create := func(name string, enabled bool) {
		body := strings.NewReader(`{"name":"` + name + `","metric":"success_rate","operator":"lt","threshold":0.9,"consecutive_buckets":2,"enabled":` + boolLiteral(enabled) + `}`)
		req := httptest.NewRequest(http.MethodPost, "/operator/v1/alert-rules", body)
		req = req.WithContext(withActor(req.Context(), actor))
		w := httptest.NewRecorder()
		createAlertRule(ctx)(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("seeding create(%s, enabled=%v): status = %d, body=%s", name, enabled, w.Code, w.Body.String())
		}
	}
	create("enabled rule", true)
	create("disabled rule", false)

	req := httptest.NewRequest(http.MethodGet, "/operator/v1/alert-rules?enabled=true", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	listAlertRules(ctx)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	var resp types.AlertRuleListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Name != "enabled rule" {
		t.Errorf("resp.Items = %+v, want exactly one item named %q", resp.Items, "enabled rule")
	}
}

func boolLiteral(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestGetUpdateDeleteAlertRuleOnAMissingIDReturn404(t *testing.T) {
	ctx := newAlertRuleTestContext()
	actor := platformActor()

	tests := []struct {
		name    string
		request func() *http.Request
		handler func(*svc.ServiceContext) http.HandlerFunc
	}{
		{
			name: "get",
			request: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/operator/v1/alert-rules/no_such_rule", nil)
			},
			handler: getAlertRule,
		},
		{
			name: "update",
			request: func() *http.Request {
				return httptest.NewRequest(http.MethodPatch, "/operator/v1/alert-rules/no_such_rule", strings.NewReader(validAlertRuleRequestBody()))
			},
			handler: updateAlertRule,
		},
		{
			name: "delete",
			request: func() *http.Request {
				return httptest.NewRequest(http.MethodDelete, "/operator/v1/alert-rules/no_such_rule", nil)
			},
			handler: deleteAlertRule,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.request()
			req = req.WithContext(withActor(req.Context(), actor))
			req = pathvar.WithVars(req, map[string]string{"id": "no_such_rule"})
			w := httptest.NewRecorder()

			tt.handler(ctx)(w, req)

			if w.Code != http.StatusNotFound {
				t.Errorf("status = %d, want %d, body=%s", w.Code, http.StatusNotFound, w.Body.String())
			}
		})
	}
}

func TestUpdateAlertRulePersistsEveryField(t *testing.T) {
	ctx := newAlertRuleTestContext()
	actor := platformActor()

	createReq := httptest.NewRequest(http.MethodPost, "/operator/v1/alert-rules", strings.NewReader(validAlertRuleRequestBody()))
	createReq = createReq.WithContext(withActor(createReq.Context(), actor))
	createW := httptest.NewRecorder()
	createAlertRule(ctx)(createW, createReq)
	if createW.Code != http.StatusCreated {
		t.Fatalf("seeding create: status = %d, body=%s", createW.Code, createW.Body.String())
	}
	var created types.AlertRuleResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}

	updateBody := `{"name":"latency too high","metric":"latency_p95","operator":"gte","threshold":2.5,"consecutive_buckets":5,"webhook_url":"https://example.com/hook2","enabled":false}`
	updateReq := httptest.NewRequest(http.MethodPatch, "/operator/v1/alert-rules/"+created.ID, strings.NewReader(updateBody))
	updateReq = updateReq.WithContext(withActor(updateReq.Context(), actor))
	updateReq = pathvar.WithVars(updateReq, map[string]string{"id": created.ID})
	updateW := httptest.NewRecorder()
	updateAlertRule(ctx)(updateW, updateReq)
	if updateW.Code != http.StatusOK {
		t.Fatalf("update: status = %d, want %d, body=%s", updateW.Code, http.StatusOK, updateW.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/operator/v1/alert-rules/"+created.ID, nil)
	getReq = getReq.WithContext(withActor(getReq.Context(), actor))
	getReq = pathvar.WithVars(getReq, map[string]string{"id": created.ID})
	getW := httptest.NewRecorder()
	getAlertRule(ctx)(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("get: status = %d, want %d, body=%s", getW.Code, http.StatusOK, getW.Body.String())
	}
	var got types.AlertRuleResponse
	if err := json.Unmarshal(getW.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal get response: %v", err)
	}

	want := types.AlertRuleResponse{
		ID: created.ID, Name: "latency too high", Metric: model.MetricLatencyP95, Operator: model.OperatorGreaterThanOrEqual,
		Threshold: 2.5, ConsecutiveBuckets: 5, WebhookURL: "https://example.com/hook2", Enabled: false,
	}
	if got.ID != want.ID || got.Name != want.Name || got.Metric != want.Metric || got.Operator != want.Operator ||
		got.Threshold != want.Threshold || got.ConsecutiveBuckets != want.ConsecutiveBuckets ||
		got.WebhookURL != want.WebhookURL || got.Enabled != want.Enabled {
		t.Errorf("get after update = %+v, want fields matching %+v", got, want)
	}
}
