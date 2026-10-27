package handler

import (
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
)

func TestMetricsHistoryHandlerRejectsMissingWindow(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	req := httptest.NewRequest(http.MethodGet, "/operator/v1/metrics/history", nil)
	w := httptest.NewRecorder()

	metricsHistory(ctx)(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for a request with no since/until", w.Code, http.StatusBadRequest)
	}
}

func TestMetricsHistoryHandlerReturnsSeries(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	since := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	if err := st.UpsertRollup(t.Context(), []model.MetricsHistoryPoint{
		{Metric: logic.HistoryMetricNames[0], Labels: "endpoint=chat,status=200", BucketAt: since.Add(time.Hour), Value: 7},
	}); err != nil {
		t.Fatalf("seeding UpsertRollup() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/operator/v1/metrics/history?since=2026-09-11T00:00:00Z&until=2026-09-12T00:00:00Z", nil)
	w := httptest.NewRecorder()
	metricsHistory(ctx)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp types.MetricsHistoryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Series) != 1 {
		t.Fatalf("resp.Series = %+v, want exactly one series", resp.Series)
	}
	if resp.Series[0].Labels["endpoint"] != "chat" || resp.Series[0].Points[0].Value != 7 {
		t.Errorf("resp.Series[0] = %+v, want labels.endpoint=chat and point value 7", resp.Series[0])
	}
}
