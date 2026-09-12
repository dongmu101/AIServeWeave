package httpapi

import "testing"

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
