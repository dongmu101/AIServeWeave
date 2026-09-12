package controlplaneclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/controlplaneclient"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

func TestPushRequestLogsPostsTheBatchAndAuthenticates(t *testing.T) {
	var gotAuth string
	var gotBody struct {
		Records []struct {
			RequestID string `json:"request_id"`
		} `json:"records"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/internal/v1/requestlogs" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s, want POST /internal/v1/requestlogs", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]int{"accepted": 1})
	}))
	defer server.Close()

	client, err := controlplaneclient.NewRequestLogsClient(controlplaneclient.RequestLogsClientConfig{Endpoint: server.URL, Token: "internal-secret"})
	if err != nil {
		t.Fatalf("NewRequestLogsClient() error = %v, want nil", err)
	}
	err = client.PushRequestLogs(context.Background(), []httpapi.RequestLogRecord{
		{RequestID: "req_1", TenantID: "tnt_1", Endpoint: "chat", StatusCode: 200, Outcome: "ok", DurationMS: 5, CreatedAt: time.Now()},
	})
	if err != nil {
		t.Fatalf("PushRequestLogs() error = %v, want nil", err)
	}
	if gotAuth != "Bearer internal-secret" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer internal-secret")
	}
	if len(gotBody.Records) != 1 || gotBody.Records[0].RequestID != "req_1" {
		t.Errorf("posted body records = %+v, want one record with request_id req_1", gotBody.Records)
	}
}

func TestPushRequestLogsOfAnEmptyBatchDoesNotCallTheServer(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer server.Close()
	client, _ := controlplaneclient.NewRequestLogsClient(controlplaneclient.RequestLogsClientConfig{Endpoint: server.URL, Token: "t"})
	if err := client.PushRequestLogs(context.Background(), nil); err != nil {
		t.Fatalf("PushRequestLogs(nil) error = %v, want nil", err)
	}
	if called {
		t.Fatal("PushRequestLogs(nil) reached the server, want it to short-circuit on an empty batch")
	}
}

func TestNewRequestLogsClientRejectsAMisconfiguredEndpointOrToken(t *testing.T) {
	tests := []struct {
		name string
		cfg  controlplaneclient.RequestLogsClientConfig
	}{
		{"no endpoint", controlplaneclient.RequestLogsClientConfig{Token: testToken}},
		{"endpoint without a scheme", controlplaneclient.RequestLogsClientConfig{Endpoint: "127.0.0.1:8090", Token: testToken}},
		{"no token", controlplaneclient.RequestLogsClientConfig{Endpoint: "http://127.0.0.1:8090"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := controlplaneclient.NewRequestLogsClient(tt.cfg); err == nil {
				t.Error("NewRequestLogsClient() = nil error, want a validation failure caught at startup rather than on the first push")
			}
		})
	}
}
