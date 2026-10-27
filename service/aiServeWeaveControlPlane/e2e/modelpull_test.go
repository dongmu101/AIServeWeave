package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// stubModelPullGateway serves one replica's -model-pull-addr listener,
// mirroring the real Gateway's modelpullapi contract: connected controls
// whether it answers 202/200 or 404, and pulls is what a GET reports.
//
// stubModelPullGateway 服务单个副本的 -model-pull-addr 监听器，与真实
// Gateway 的 modelpullapi 契约一致：connected 决定它回 202/200 还是 404，
// pulls 是 GET 所报告的内容。
func stubModelPullGateway(t *testing.T, connected bool, pulls []map[string]any) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+modelPullToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !connected {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"generated_at": "2026-01-01T00:00:00Z", "pulls": pulls})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// TestModelPullForwardingRoundTrip drives the full HTTP stack — real routing,
// real platform session middleware, the real modelpullrouter fan-out — for
// both the trigger and status endpoints, against a node connected to one of
// two configured replicas.
//
// TestModelPullForwardingRoundTrip 驱动完整的 HTTP 栈——真实路由、真实平台
// 会话中间件、真实的 modelpullrouter 扇出——分别覆盖触发与状态两个端点，
// 针对一个连到两个已配置副本之一的节点。
func TestModelPullForwardingRoundTrip(t *testing.T) {
	elsewhere := stubModelPullGateway(t, false, nil)
	here := stubModelPullGateway(t, true, []map[string]any{
		{"name": "qwen3-coder:30b", "state": "downloading", "bytes_downloaded": 1024, "bytes_total": 4096, "updated_at": "2026-01-01T00:00:00Z"},
	})

	h := newHarnessWithModelPull(t, []string{elsewhere, here})
	platform := bootstrapPlatformOperator(h, "operator@example.com")

	var trigger types.ModelPullTriggerResponse
	status := h.call(http.MethodPost, "/operator/v1/nodes/node-a/model-pulls", platform, types.ModelPullTriggerRequest{Names: []string{"qwen3-coder:30b"}}, &trigger)
	if status != http.StatusAccepted {
		t.Fatalf("trigger status = %d, want 202", status)
	}
	if len(trigger.Replicas) != 2 {
		t.Fatalf("trigger reported %d replicas, want 2", len(trigger.Replicas))
	}

	var statusResp types.ModelPullStatusResponse
	status = h.call(http.MethodGet, "/operator/v1/nodes/node-a/model-pulls", platform, nil, &statusResp)
	if status != http.StatusOK {
		t.Fatalf("status status = %d, want 200", status)
	}
	if len(statusResp.Pulls) != 1 || statusResp.Pulls[0].Name != "qwen3-coder:30b" || statusResp.Pulls[0].State != "downloading" {
		t.Fatalf("pulls = %+v, want the one entry the connected replica reported", statusResp.Pulls)
	}
}

// TestModelPullForwardingUnknownNodeIs404 asserts a node connected to none
// of the configured replicas is reported as not found, on both endpoints.
//
// TestModelPullForwardingUnknownNodeIs404 断言一个没有连到任何已配置副本的
// 节点，在两个端点上都被报告为未找到。
func TestModelPullForwardingUnknownNodeIs404(t *testing.T) {
	nowhere := stubModelPullGateway(t, false, nil)
	h := newHarnessWithModelPull(t, []string{nowhere})
	platform := bootstrapPlatformOperator(h, "operator@example.com")

	if status := h.call(http.MethodPost, "/operator/v1/nodes/ghost/model-pulls", platform, types.ModelPullTriggerRequest{Names: []string{"m"}}, nil); status != http.StatusNotFound {
		t.Errorf("trigger status = %d, want 404", status)
	}
	if status := h.call(http.MethodGet, "/operator/v1/nodes/ghost/model-pulls", platform, nil, nil); status != http.StatusNotFound {
		t.Errorf("status status = %d, want 404", status)
	}
}

// TestModelPullForwardingRejectsATenantSession asserts a tenant session
// cannot reach either endpoint — a node has no tenant, the same reasoning
// the node-ops and fleet endpoints already enforce.
//
// TestModelPullForwardingRejectsATenantSession 断言租户会话无法访问这两个
// 端点——节点没有租户，与节点操作、机群端点已经强制的理由相同。
func TestModelPullForwardingRejectsATenantSession(t *testing.T) {
	here := stubModelPullGateway(t, true, nil)
	h := newHarnessWithModelPull(t, []string{here})
	_, tenantSession := bootstrap(h, "Acme", "owner@example.com")

	if status := h.call(http.MethodPost, "/operator/v1/nodes/node-a/model-pulls", tenantSession, types.ModelPullTriggerRequest{Names: []string{"m"}}, nil); status != http.StatusForbidden {
		t.Errorf("trigger status = %d, want 403", status)
	}
}

// TestModelPullForwardingNotMountedWhenUnconfigured asserts a deployment
// that never configured ModelPull has no route at all, not one that answers
// "not configured" — the same rule Fleet and RegistryClient already follow.
//
// TestModelPullForwardingNotMountedWhenUnconfigured 断言一个从未配置
// ModelPull 的部署根本没有这条路由，而不是有一条回答"未配置"的路由——与
// Fleet、RegistryClient 已经遵循的同一规则。
func TestModelPullForwardingNotMountedWhenUnconfigured(t *testing.T) {
	h := newHarness(t)
	platform := bootstrapPlatformOperator(h, "operator@example.com")

	if status := h.call(http.MethodGet, "/operator/v1/nodes/node-a/model-pulls", platform, nil, nil); status != http.StatusNotFound {
		t.Errorf("status status = %d, want 404 for an unmounted route", status)
	}
}
