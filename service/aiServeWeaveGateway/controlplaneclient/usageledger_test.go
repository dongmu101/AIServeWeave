package controlplaneclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/pricing"
	"AIServeWeave/service/aiServeWeaveGateway/controlplaneclient"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

func TestPushUsageRecordsPostsTheBatchAndAuthenticates(t *testing.T) {
	var gotAuth string
	var gotBody struct {
		Records []map[string]json.RawMessage `json:"records"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/internal/v1/usagerecords" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s, want POST /internal/v1/usagerecords", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]int{"accepted": 2})
	}))
	defer server.Close()

	client, err := controlplaneclient.NewUsageLedgerClient(controlplaneclient.UsageLedgerClientConfig{Endpoint: server.URL, Token: "internal-secret"})
	if err != nil {
		t.Fatalf("NewUsageLedgerClient() error = %v, want nil", err)
	}
	err = client.PushUsageRecords(context.Background(), []httpapi.UsageRecord{
		{RequestID: "req_1", TenantID: "tnt_1", Model: "llama3", Endpoint: "chat", PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, CreatedAt: time.Now(), KeyID: "key_1",
			Details: map[string]int64{"input.cache_read": 6}, Attributes: map[string]string{"service_tier": "flex"}},
		{RequestID: "req_2", TenantID: "tnt_1", Model: "llama3", Endpoint: "chat", PromptTokens: 1, TotalTokens: 1, CreatedAt: time.Now()},
	})
	if err != nil {
		t.Fatalf("PushUsageRecords() error = %v, want nil", err)
	}
	if gotAuth != "Bearer internal-secret" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer internal-secret")
	}
	if len(gotBody.Records) != 2 {
		t.Fatalf("posted %d records, want 2", len(gotBody.Records))
	}
	first, second := gotBody.Records[0], gotBody.Records[1]
	if string(first["request_id"]) != `"req_1"` || string(first["key_id"]) != `"key_1"` ||
		string(first["details"]) != `{"input.cache_read":6}` || string(first["attributes"]) != `{"service_tier":"flex"}` {
		t.Errorf("first posted record = %s, want request_id req_1, key_id key_1 and its details and attributes", first)
	}
	// A control plane that predates these fields refuses them, so empty
	// ones must be left out.
	// 不认识这些字段的旧控制面会拒绝它们，因此为空时必须省略。
	for _, field := range []string{"key_id", "node_id", "details", "attributes"} {
		if _, present := second[field]; present {
			t.Errorf("second posted record carries an empty %s field: %s", field, second)
		}
	}
}

func TestPushUsageRecordsReportsAPartialAcceptance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"accepted":1}`)
	}))
	defer server.Close()
	client, err := controlplaneclient.NewUsageLedgerClient(controlplaneclient.UsageLedgerClientConfig{Endpoint: server.URL, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	err = client.PushUsageRecords(context.Background(), []httpapi.UsageRecord{{RequestID: "req_a"}, {RequestID: "req_b"}})
	if !errors.Is(err, httpapi.ErrUsageBatchRejected) {
		t.Fatalf("partial acceptance returned %v, want a rejected batch", err)
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
	const controlPlaneMaxUsageRecordsBodyBytes = 4 << 20

	var gotBodyBytes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading the request body: %v", err)
		}
		gotBodyBytes = len(body)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]int{"accepted": httpapi.DefaultUsageLedgerBatchSize})
	}))
	defer server.Close()

	client, err := controlplaneclient.NewUsageLedgerClient(controlplaneclient.UsageLedgerClientConfig{Endpoint: server.URL, Token: testToken})
	if err != nil {
		t.Fatalf("NewUsageLedgerClient() error = %v, want nil", err)
	}

	// Every record carries the most details and attributes common/pricing
	// allows, with the longest names, values and counts, so the batch is the
	// largest a Gateway can send.
	// 每条记录都带上 common/pricing 允许的最多明细与属性，名称、取值与计数都取最长，
	// 使这一批成为 Gateway 可能发出的最大批次。
	details := make(map[string]int64, pricing.MaxDetails)
	for i := 0; i < pricing.MaxDetails; i++ {
		name := fmt.Sprintf("input.m%02d", i)
		details[name+strings.Repeat("x", pricing.MaxMeterNameLen-len(name))] = math.MaxInt64
	}
	attributes := make(map[string]string, pricing.MaxAttributes)
	for i := 0; i < pricing.MaxAttributes; i++ {
		key := fmt.Sprintf("a%02d", i)
		attributes[key+strings.Repeat("k", pricing.MaxAttributeKeyLen-len(key))] = strings.Repeat("v", pricing.MaxAttributeValLen)
	}
	if pricing.ValidateDetails(details) != nil || pricing.ValidateAttributes(attributes) != nil {
		t.Fatal("the worst-case details must be valid")
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
			KeyID:            fmt.Sprintf("key_%036x", i),
			Details:          details,
			Attributes:       attributes,
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
