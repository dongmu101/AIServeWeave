package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"

	"github.com/zeromicro/go-zero/rest/pathvar"
)

// newAlertInstanceTestContext returns a ServiceContext backed by a fresh
// memstore, plus the memstore itself so a test can seed alert rules and
// instances directly — there is no POST /alerts endpoint, since instances
// are created by the evaluation loop, not a platform actor.
//
// newAlertInstanceTestContext 返回一个基于全新 memstore 的 ServiceContext，
// 以及 memstore 本身，供测试直接写入告警规则与实例——不存在 POST /alerts
// 端点，因为实例由评估循环创建，而不是由平台身份创建。
func newAlertInstanceTestContext() (*svc.ServiceContext, *memstore.Store) {
	st := memstore.New()
	return &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}, st
}

func TestListAlertsFiltersAndFillsRuleName(t *testing.T) {
	ctx, st := newAlertInstanceTestContext()
	actor := platformActor()

	ruleCPU := model.AlertRule{ID: model.NewID(model.PrefixAlertRule), Name: "cpu high", Metric: model.MetricSuccessRate, Operator: model.OperatorLessThan, Threshold: 0.9, ConsecutiveBuckets: 3, Enabled: true}
	ruleMem := model.AlertRule{ID: model.NewID(model.PrefixAlertRule), Name: "mem high", Metric: model.MetricSuccessRate, Operator: model.OperatorLessThan, Threshold: 0.9, ConsecutiveBuckets: 3, Enabled: true}
	if err := st.CreateAlertRule(context.Background(), &ruleCPU); err != nil {
		t.Fatalf("seed rule cpu: %v", err)
	}
	if err := st.CreateAlertRule(context.Background(), &ruleMem); err != nil {
		t.Fatalf("seed rule mem: %v", err)
	}

	firing := model.AlertInstance{
		ID: model.NewID(model.PrefixAlertInstance), RuleID: ruleCPU.ID, Status: model.AlertStatusFiring,
		ValueAtFire: 0.5, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), LastEvaluatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotifyStatus: "sent",
	}
	resolved := model.AlertInstance{
		ID: model.NewID(model.PrefixAlertInstance), RuleID: ruleMem.ID, Status: model.AlertStatusResolved,
		ValueAtFire: 0.6, CreatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), LastEvaluatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		NotifyStatus: "sent",
	}
	if err := st.CreateAlertInstance(context.Background(), &firing); err != nil {
		t.Fatalf("seed firing instance: %v", err)
	}
	if err := st.CreateAlertInstance(context.Background(), &resolved); err != nil {
		t.Fatalf("seed resolved instance: %v", err)
	}

	tests := []struct {
		name      string
		query     string
		wantIDs   []string
		wantNames map[string]string // instance ID -> expected RuleName
	}{
		{
			name:      "filters by status",
			query:     "status=firing",
			wantIDs:   []string{firing.ID},
			wantNames: map[string]string{firing.ID: "cpu high"},
		},
		{
			name:      "filters by rule_id",
			query:     "rule_id=" + ruleMem.ID,
			wantIDs:   []string{resolved.ID},
			wantNames: map[string]string{resolved.ID: "mem high"},
		},
		{
			name:      "no filter returns both and enriches every RuleName",
			query:     "",
			wantIDs:   []string{resolved.ID, firing.ID}, // newest first
			wantNames: map[string]string{firing.ID: "cpu high", resolved.ID: "mem high"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := "/operator/v1/alerts"
			if tt.query != "" {
				target += "?" + tt.query
			}
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req = req.WithContext(withActor(req.Context(), actor))
			w := httptest.NewRecorder()

			listAlerts(ctx)(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
			}
			var resp types.AlertInstanceListResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			gotIDs := make([]string, len(resp.Items))
			gotNames := map[string]string{}
			for i, item := range resp.Items {
				gotIDs[i] = item.ID
				gotNames[item.ID] = item.RuleName
			}
			if len(gotIDs) != len(tt.wantIDs) {
				t.Fatalf("got %d items (%v), want %d items (%v)", len(gotIDs), gotIDs, len(tt.wantIDs), tt.wantIDs)
			}
			for i, want := range tt.wantIDs {
				if gotIDs[i] != want {
					t.Errorf("items[%d].ID = %q, want %q (full got=%v, want=%v)", i, gotIDs[i], want, gotIDs, tt.wantIDs)
				}
			}
			for id, wantName := range tt.wantNames {
				if got := gotNames[id]; got != wantName {
					t.Errorf("RuleName for instance %q = %q, want %q", id, got, wantName)
				}
			}
		})
	}
}

func TestListAlertsRejectsAMalformedSinceOrUntil(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{name: "malformed since", query: "since=not-a-timestamp"},
		{name: "malformed until", query: "until=not-a-timestamp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := newAlertInstanceTestContext()
			req := httptest.NewRequest(http.MethodGet, "/operator/v1/alerts?"+tt.query, nil)
			req = req.WithContext(withActor(req.Context(), platformActor()))
			w := httptest.NewRecorder()

			listAlerts(ctx)(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d, body=%s", w.Code, http.StatusBadRequest, w.Body.String())
			}
		})
	}
}

func TestAcknowledgeAlertOnAResolvedInstanceReturnsTheConflictMapping(t *testing.T) {
	ctx, st := newAlertInstanceTestContext()
	actor := platformActor()

	rule := model.AlertRule{ID: model.NewID(model.PrefixAlertRule), Name: "cpu high", Metric: model.MetricSuccessRate, Operator: model.OperatorLessThan, Threshold: 0.9, ConsecutiveBuckets: 3, Enabled: true}
	if err := st.CreateAlertRule(context.Background(), &rule); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	resolved := model.AlertInstance{
		ID: model.NewID(model.PrefixAlertInstance), RuleID: rule.ID, Status: model.AlertStatusResolved,
		ValueAtFire: 0.5, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), LastEvaluatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotifyStatus: "sent",
	}
	if err := st.CreateAlertInstance(context.Background(), &resolved); err != nil {
		t.Fatalf("seed resolved instance: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/operator/v1/alerts/"+resolved.ID+"/acknowledge", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	req = pathvar.WithVars(req, map[string]string{"id": resolved.ID})
	w := httptest.NewRecorder()

	acknowledgeAlert(ctx)(w, req)

	// respondErr maps logic.ErrConflict (which translate() produces from
	// store.ErrConflict) to http.StatusConflict — see respondErr's mapping
	// table in handlers.go. This test asserts against that existing
	// mapping rather than a status code invented for this one handler.
	//
	// respondErr 把 logic.ErrConflict（由 translate() 从 store.ErrConflict
	// 转译而来）映射到 http.StatusConflict —— 见 handlers.go 里 respondErr
	// 现有的映射表。本测试断言的是这张既有映射，而不是为这一个 handler
	// 另外发明的状态码。
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d, body=%s", w.Code, http.StatusConflict, w.Body.String())
	}
}

func TestAcknowledgeAlertOnAFiringInstanceSucceeds(t *testing.T) {
	ctx, st := newAlertInstanceTestContext()
	actor := platformActor()

	rule := model.AlertRule{ID: model.NewID(model.PrefixAlertRule), Name: "cpu high", Metric: model.MetricSuccessRate, Operator: model.OperatorLessThan, Threshold: 0.9, ConsecutiveBuckets: 3, Enabled: true}
	if err := st.CreateAlertRule(context.Background(), &rule); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	firing := model.AlertInstance{
		ID: model.NewID(model.PrefixAlertInstance), RuleID: rule.ID, Status: model.AlertStatusFiring,
		ValueAtFire: 0.5, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), LastEvaluatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotifyStatus: "sent",
	}
	if err := st.CreateAlertInstance(context.Background(), &firing); err != nil {
		t.Fatalf("seed firing instance: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/operator/v1/alerts/"+firing.ID+"/acknowledge", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	req = pathvar.WithVars(req, map[string]string{"id": firing.ID})
	w := httptest.NewRecorder()

	acknowledgeAlert(ctx)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	var resp types.AlertInstanceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Status != model.AlertStatusAcknowledged || resp.AcknowledgedBy != actor.UserID {
		t.Errorf("resp = %+v, want Status=%q AcknowledgedBy=%q", resp, model.AlertStatusAcknowledged, actor.UserID)
	}
}
