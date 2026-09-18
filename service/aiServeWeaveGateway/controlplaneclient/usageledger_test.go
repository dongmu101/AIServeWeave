package controlplaneclient_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/controlplaneclient"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

func TestPushUsageRecordsPostsTheBatchAndAuthenticates(t *testing.T) {
	var gotAuth string
	var gotBody struct {
		Records []struct {
			RequestID string `json:"request_id"`
		} `json:"records"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/internal/v1/usagerecords" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s, want POST /internal/v1/usagerecords", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]int{"accepted": 1})
	}))
	defer server.Close()

	client, err := controlplaneclient.NewUsageLedgerClient(controlplaneclient.UsageLedgerClientConfig{Endpoint: server.URL, Token: "internal-secret"})
	if err != nil {
		t.Fatalf("NewUsageLedgerClient() error = %v, want nil", err)
	}
	err = client.PushUsageRecords(context.Background(), []httpapi.UsageRecord{
		{RequestID: "req_1", TenantID: "tnt_1", Model: "llama3", Endpoint: "chat", PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, CreatedAt: time.Now()},
	})
	if err != nil {
		t.Fatalf("PushUsageRecords() error = %v, want nil", err)
	}
	if gotAuth != "Bearer internal-secret" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer internal-secret")
	}
	if len(gotBody.Records) != 1 || gotBody.Records[0].RequestID != "req_1" {
		t.Errorf("posted body records = %+v, want one record with request_id req_1", gotBody.Records)
	}
}

func TestPushUsageRecordsOfAnEmptyBatchDoesNotCallTheServer(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer server.Close()
	client, _ := controlplaneclient.NewUsageLedgerClient(controlplaneclient.UsageLedgerClientConfig{Endpoint: server.URL, Token: "t"})
	if err := client.PushUsageRecords(context.Background(), nil); err != nil {
		t.Fatalf("PushUsageRecords(nil) error = %v, want nil", err)
	}
	if called {
		t.Fatal("PushUsageRecords(nil) reached the server, want it to short-circuit on an empty batch")
	}
}

// TestPushUsageRecordsMaxBatchFitsTheControlPlanesBodyLimit guards the same
// kind of cross-service contract TestPushRequestLogsMaxBatchFitsTheControlPlanesBodyLimit
// already guards for request logs: the control plane's
// POST /internal/v1/usagerecords handler bounds its request body to
// handler.MaxUsageRecordsBodyBytes, sized for a full
// httpapi.DefaultUsageLedgerBatchSize batch with headroom.
//
// TestPushUsageRecordsMaxBatchFitsTheControlPlanesBodyLimit 守护与
// TestPushRequestLogsMaxBatchFitsTheControlPlanesBodyLimit 同一类跨服务
// 契约：控制面 POST /internal/v1/usagerecords 的 handler 把请求体限制在
// handler.MaxUsageRecordsBodyBytes 以内，这个值按一整批
// httpapi.DefaultUsageLedgerBatchSize 记录留有余量设定。
func TestPushUsageRecordsMaxBatchFitsTheControlPlanesBodyLimit(t *testing.T) {
	// controlPlaneMaxUsageRecordsBodyBytes mirrors the control plane's
	// handler.MaxUsageRecordsBodyBytes — duplicated here, not imported, for
	// the same cross-service reason TestPushRequestLogsMaxBatchFitsTheControlPlanesBodyLimit
	// already documents.
	const controlPlaneMaxUsageRecordsBodyBytes = 256 << 10

	var gotBodyBytes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading the request body: %v", err)
		}
		gotBodyBytes = len(body)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]int{"accepted": len(body)})
	}))
	defer server.Close()

	client, err := controlplaneclient.NewUsageLedgerClient(controlplaneclient.UsageLedgerClientConfig{Endpoint: server.URL, Token: testToken})
	if err != nil {
		t.Fatalf("NewUsageLedgerClient() error = %v, want nil", err)
	}

	now := time.Now()
	records := make([]httpapi.UsageRecord, httpapi.DefaultUsageLedgerBatchSize)
	for i := range records {
		records[i] = httpapi.UsageRecord{
			RequestID:        fmt.Sprintf("%032x", i),
			TenantID:         fmt.Sprintf("tnt_%08x", i),
			Model:            "some-long-registered-model-alias-name",
			Endpoint:         "anthropic_messages",
			PromptTokens:     123456,
			CompletionTokens: 123456,
			TotalTokens:      246912,
			CreatedAt:        now,
		}
	}
	if err := client.PushUsageRecords(context.Background(), records); err != nil {
		t.Fatalf("PushUsageRecords() error = %v, want nil", err)
	}

	if gotBodyBytes >= controlPlaneMaxUsageRecordsBodyBytes {
		t.Fatalf("a full batch of %d records serialized to %d bytes, want comfortably under the control plane's %d byte limit (handler.MaxUsageRecordsBodyBytes)",
			httpapi.DefaultUsageLedgerBatchSize, gotBodyBytes, controlPlaneMaxUsageRecordsBodyBytes)
	}
	t.Logf("a full batch of %d records serialized to %d bytes (control plane limit %d)",
		httpapi.DefaultUsageLedgerBatchSize, gotBodyBytes, controlPlaneMaxUsageRecordsBodyBytes)
}

func TestNewUsageLedgerClientRejectsAMisconfiguredEndpointOrToken(t *testing.T) {
	tests := []struct {
		name string
		cfg  controlplaneclient.UsageLedgerClientConfig
	}{
		{"no endpoint", controlplaneclient.UsageLedgerClientConfig{Token: testToken}},
		{"endpoint without a scheme", controlplaneclient.UsageLedgerClientConfig{Endpoint: "127.0.0.1:8090", Token: testToken}},
		{"no token", controlplaneclient.UsageLedgerClientConfig{Endpoint: "http://127.0.0.1:8090"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := controlplaneclient.NewUsageLedgerClient(tt.cfg); err == nil {
				t.Error("NewUsageLedgerClient() = nil error, want a validation failure caught at startup rather than on the first push")
			}
		})
	}
}
