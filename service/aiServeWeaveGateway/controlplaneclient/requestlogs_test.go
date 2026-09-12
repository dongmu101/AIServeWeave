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

// TestPushRequestLogsMaxBatchFitsTheControlPlanesBodyLimit guards a
// cross-service contract: the control plane's POST /internal/v1/requestlogs
// handler bounds its request body to handler.MaxRequestLogsBodyBytes
// (service/aiServeWeaveControlPlane/internal/handler/handlers.go, 256 KiB),
// sized specifically for a full httpapi.DefaultRequestLogBatchSize batch
// with headroom. Before that constant existed, a full batch of 500 records
// (~112 KB) silently exceeded the package's shared 64 KiB decode limit, the
// control plane returned 400, and requestLogPusher.flush dropped the whole
// batch without retrying — invisible at low request volume and only
// happening once a Gateway replica sustained real load. This test marshals
// a full batch through the real client and fails if the wire size ever
// creeps back up to the control plane's limit, so a change to either
// constant is caught here rather than by a production replica silently
// losing its search history again.
//
// TestPushRequestLogsMaxBatchFitsTheControlPlanesBodyLimit 守护一份跨服务
// 契约：控制面 POST /internal/v1/requestlogs 的 handler 把请求体限制在
// handler.MaxRequestLogsBodyBytes（service/aiServeWeaveControlPlane/
// internal/handler/handlers.go，256 KiB）以内，这个值专门按一整批
// httpapi.DefaultRequestLogBatchSize 记录留有余量设定。在这个常量出现之前，
// 一整批 500 条记录（约 112 KB）会悄悄超出本包共用的 64 KiB decode 上限，
// 控制面返回 400，requestLogPusher.flush 会把整批丢弃且不重试——在低请求量
// 下完全不可见，只在某个 Gateway 副本承受真实负载时才会发生。本测试通过真实
// 客户端序列化一整批记录，一旦线上体积重新逼近控制面的上限就会失败，从而让
// 任一常量的改动在这里被发现，而不是让某个生产副本悄悄再次丢失检索历史。
func TestPushRequestLogsMaxBatchFitsTheControlPlanesBodyLimit(t *testing.T) {
	// controlPlaneMaxRequestLogsBodyBytes mirrors the control plane's
	// handler.MaxRequestLogsBodyBytes. It is duplicated here, not imported,
	// because the Gateway and the control plane are separate services talking
	// over HTTP, not a shared Go package — see this repo's AGENTS.md on
	// contract packages. Keep the two in step by hand.
	//
	// controlPlaneMaxRequestLogsBodyBytes 对应控制面的
	// handler.MaxRequestLogsBodyBytes。它在这里是重复定义而不是导入，因为
	// Gateway 与控制面是通过 HTTP 对话的两个独立服务，而不是共享的 Go 包——见
	// 本仓库 AGENTS.md 关于契约包的说明。这两处需要手动保持一致。
	const controlPlaneMaxRequestLogsBodyBytes = 256 << 10

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

	client, err := controlplaneclient.NewRequestLogsClient(controlplaneclient.RequestLogsClientConfig{Endpoint: server.URL, Token: testToken})
	if err != nil {
		t.Fatalf("NewRequestLogsClient() error = %v, want nil", err)
	}

	// Field sizes mirror the reviewer's realistic worst case: a 32-hex
	// request_id, a "tnt_xxxxxxxx" tenant id, an "aisw-xxxxxxxx" key display,
	// the longest built-in endpoint and outcome values, and an RFC3339Nano
	// timestamp.
	//
	// 字段大小对照评审所用的真实最坏情形：32 位十六进制 request_id、
	// "tnt_xxxxxxxx" 形式的租户 id、"aisw-xxxxxxxx" 形式的 key 展示形式、
	// 最长的内置 endpoint 与 outcome 取值，以及一个 RFC3339Nano 时间戳。
	now := time.Now()
	records := make([]httpapi.RequestLogRecord, httpapi.DefaultRequestLogBatchSize)
	for i := range records {
		records[i] = httpapi.RequestLogRecord{
			RequestID:  fmt.Sprintf("%032x", i),
			TenantID:   fmt.Sprintf("tnt_%08x", i),
			KeyDisplay: fmt.Sprintf("aisw-%08x", i),
			Endpoint:   "embeddings",
			StatusCode: 200,
			Outcome:    "upstream_unavailable",
			DurationMS: 123456,
			CreatedAt:  now,
		}
	}
	if err := client.PushRequestLogs(context.Background(), records); err != nil {
		t.Fatalf("PushRequestLogs() error = %v, want nil", err)
	}

	if gotBodyBytes >= controlPlaneMaxRequestLogsBodyBytes {
		t.Fatalf("a full batch of %d records serialized to %d bytes, want comfortably under the control plane's %d byte limit (handler.MaxRequestLogsBodyBytes)",
			httpapi.DefaultRequestLogBatchSize, gotBodyBytes, controlPlaneMaxRequestLogsBodyBytes)
	}
	t.Logf("a full batch of %d records serialized to %d bytes (control plane limit %d)",
		httpapi.DefaultRequestLogBatchSize, gotBodyBytes, controlPlaneMaxRequestLogsBodyBytes)
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
