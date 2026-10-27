package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"AIServeWeave/common/metrics/metricstest"
	cpmetrics "AIServeWeave/service/aiServeWeaveControlPlane/internal/metrics"
)

func TestInstrumentedRecordsRequestsByRouteTemplate(t *testing.T) {
	mx := metricstest.New()
	h := instrumented(mx, "/admin/v1/jobs/history/:id", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	h(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/admin/v1/jobs/history/job-123", nil))

	if got := mx.Sum(cpmetrics.MetricHTTPRequestsTotal, map[string]string{"route": "/admin/v1/jobs/history/:id", "status": "200"}); got != 1 {
		t.Errorf("request count = %v, want 1", got)
	}
}

func TestInstrumentedNilRegistrySkipsRecording(t *testing.T) {
	called := false
	h := instrumented(nil, "/admin/v1/audit", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	h(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/admin/v1/audit", nil))

	if !called {
		t.Error("wrapped handler was not called when the registry is nil")
	}
}
