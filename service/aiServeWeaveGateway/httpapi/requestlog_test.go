package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"AIServeWeave/common/runtime"
)

func TestOutcomeForStatusIsAClosedMapping(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   string
	}{
		{name: "200 is ok", status: 200, want: OutcomeOK},
		{name: "201 is ok", status: 201, want: OutcomeOK},
		{name: "400 is invalid_request", status: 400, want: OutcomeInvalidRequest},
		{name: "401 is unauthorized", status: 401, want: OutcomeUnauthorized},
		{name: "403 is forbidden", status: 403, want: OutcomeForbidden},
		{name: "404 is not_found", status: 404, want: OutcomeNotFound},
		{name: "429 is rate_limited", status: 429, want: OutcomeRateLimited},
		{name: "500 is internal", status: 500, want: OutcomeInternal},
		{name: "502 is upstream_unavailable", status: 502, want: OutcomeUpstreamUnavailable},
		{name: "503 is upstream_unavailable", status: 503, want: OutcomeUpstreamUnavailable},
		{name: "504 is upstream_unavailable", status: 504, want: OutcomeUpstreamUnavailable},
		{name: "an unrecognized status falls back to error", status: 418, want: OutcomeError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := outcomeForStatus(tt.status); got != tt.want {
				t.Fatalf("outcomeForStatus(%d) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestRequestLogEndpointOnlyMatchesTheFourFrontDoorRoutes(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		wantEP string
		wantOK bool
	}{
		{name: "models", path: "/v1/models", wantEP: "models", wantOK: true},
		{name: "chat completions", path: "/v1/chat/completions", wantEP: "chat", wantOK: true},
		{name: "embeddings", path: "/v1/embeddings", wantEP: "embeddings", wantOK: true},
		{name: "responses", path: "/v1/responses", wantEP: "responses", wantOK: true},
		{name: "job status is out of scope", path: "/v1/jobs/job_1", wantOK: false},
		{name: "workflow run submission is out of scope", path: "/v1/workflows/wf_1/runs", wantOK: false},
		{name: "an unrecognized path is out of scope", path: "/wp-admin.php", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotEP, gotOK := requestLogEndpoint(tt.path)
			if gotOK != tt.wantOK || (gotOK && gotEP != tt.wantEP) {
				t.Fatalf("requestLogEndpoint(%q) = (%q, %v), want (%q, %v)", tt.path, gotEP, gotOK, tt.wantEP, tt.wantOK)
			}
		})
	}
}

type fakeRequestLogSink struct {
	records []RequestLogRecord
}

func (s *fakeRequestLogSink) enqueue(r RequestLogRecord) bool {
	s.records = append(s.records, r)
	return true
}

func TestRequestLogMiddlewareRecordsOnlyAuthenticatedFrontDoorRequests(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		withIdentity bool
		wantRecorded bool
	}{
		{name: "an authenticated chat request is recorded", path: "/v1/chat/completions", withIdentity: true, wantRecorded: true},
		{name: "no identity in context means no tenant, so nothing is recorded", path: "/v1/chat/completions", withIdentity: false, wantRecorded: false},
		{name: "an authenticated job route is out of scope", path: "/v1/jobs/job_1", withIdentity: true, wantRecorded: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &fakeRequestLogSink{}
			h := &handlers{requestLogs: sink, metrics: newRecorder(nil), clock: runtime.NewSystemClock()}
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			req.Header.Set("Authorization", "Bearer aisw-testkeyplaintextvalue")
			ctx := req.Context()
			if tt.withIdentity {
				ctx = context.WithValue(ctx, identityKey{}, Identity{TenantID: "tnt_1", KeyID: "key_1"})
			}
			rec := httptest.NewRecorder()
			h.requestLogMiddleware(next).ServeHTTP(rec, req.WithContext(ctx))

			if got := len(sink.records) == 1; got != tt.wantRecorded {
				t.Fatalf("recorded a request = %v (records=%v), want %v", got, sink.records, tt.wantRecorded)
			}
		})
	}
}

func TestRequestLogMiddlewareFieldsMatchTheRequest(t *testing.T) {
	sink := &fakeRequestLogSink{}
	h := &handlers{requestLogs: sink, metrics: newRecorder(nil), clock: runtime.NewSystemClock()}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTooManyRequests) })

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", nil)
	req.Header.Set("Authorization", "Bearer aisw-abcdefgh12345678")
	ctx := context.WithValue(req.Context(), identityKey{}, Identity{TenantID: "tnt_9", KeyID: "key_9"})
	rec := httptest.NewRecorder()
	h.requestLogMiddleware(next).ServeHTTP(rec, req.WithContext(ctx))

	if len(sink.records) != 1 {
		t.Fatalf("got %d records, want 1", len(sink.records))
	}
	got := sink.records[0]
	if got.TenantID != "tnt_9" {
		t.Errorf("TenantID = %q, want tnt_9", got.TenantID)
	}
	if got.Endpoint != requestLogEndpointEmbeddings {
		t.Errorf("Endpoint = %q, want %q", got.Endpoint, requestLogEndpointEmbeddings)
	}
	if got.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want %d", got.StatusCode, http.StatusTooManyRequests)
	}
	if got.Outcome != OutcomeRateLimited {
		t.Errorf("Outcome = %q, want %q", got.Outcome, OutcomeRateLimited)
	}
	if got.KeyDisplay == "" || got.KeyDisplay == "aisw-abcdefgh12345678" {
		t.Errorf("KeyDisplay = %q, want a non-empty display form that is not the full plaintext key", got.KeyDisplay)
	}
}

func TestRequestLogMiddlewareIsANoOpWhenNoSinkIsConfigured(t *testing.T) {
	h := &handlers{requestLogs: nil, metrics: newRecorder(nil), clock: runtime.NewSystemClock()}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx := context.WithValue(req.Context(), identityKey{}, Identity{TenantID: "tnt_1"})
	rec := httptest.NewRecorder()
	// Must not panic with a nil sink — this is the "no control plane
	// configured" degrade path every other background feature in this
	// package already follows.
	h.requestLogMiddleware(next).ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the wrapped handler must still run)", rec.Code)
	}
}
