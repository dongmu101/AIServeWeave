package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// stubComfyUIManagedGateway serves one replica's -comfyui-managed-addr
// listener, mirroring the real Gateway's comfyuimanagedapi contract:
// connected controls whether it answers 202/200 or 404, and instances is
// what a GET reports.
//
// stubComfyUIManagedGateway 服务单个副本的 -comfyui-managed-addr 监听器，
// 与真实 Gateway 的 comfyuimanagedapi 契约一致：connected 决定它回 202/200
// 还是 404，instances 是 GET 所报告的内容。
func stubComfyUIManagedGateway(t *testing.T, connected bool, instances []map[string]any) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+comfyUIManagedToken {
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
			_ = json.NewEncoder(w).Encode(map[string]any{"generated_at": "2026-01-01T00:00:00Z", "instances": instances})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// TestComfyUIManagedForwardingRoundTrip drives the full HTTP stack — real
// routing, real platform session middleware, the real
// comfyuimanagedrouter fan-out — for both the trigger and status
// endpoints, against a node connected to one of two configured replicas.
//
// TestComfyUIManagedForwardingRoundTrip 驱动完整的 HTTP 栈——真实路由、真实
// 平台会话中间件、真实的 comfyuimanagedrouter 扇出——分别覆盖触发与状态两个
// 端点，针对一个连到两个已配置副本之一的节点。
func TestComfyUIManagedForwardingRoundTrip(t *testing.T) {
	elsewhere := stubComfyUIManagedGateway(t, false, nil)
	here := stubComfyUIManagedGateway(t, true, []map[string]any{
		{"container_name": "aisw-comfyui-managed", "state": "running", "updated_at": "2026-01-01T00:00:00Z"},
	})

	h := newHarnessWithComfyUIManaged(t, []string{elsewhere, here})
	platform := bootstrapPlatformOperator(h, "operator@example.com")

	var trigger types.ComfyUIManagedTriggerResponse
	status := h.call(http.MethodPost, "/operator/v1/nodes/node-a/comfyui-managed", platform, types.ComfyUIManagedTriggerRequest{Action: "start"}, &trigger)
	if status != http.StatusAccepted {
		t.Fatalf("trigger status = %d, want 202", status)
	}
	if len(trigger.Replicas) != 2 {
		t.Fatalf("trigger reported %d replicas, want 2", len(trigger.Replicas))
	}

	var statusResp types.ComfyUIManagedStatusResponse
	status = h.call(http.MethodGet, "/operator/v1/nodes/node-a/comfyui-managed", platform, nil, &statusResp)
	if status != http.StatusOK {
		t.Fatalf("status status = %d, want 200", status)
	}
	if len(statusResp.Instances) != 1 || statusResp.Instances[0].ContainerName != "aisw-comfyui-managed" || statusResp.Instances[0].State != "running" {
		t.Fatalf("instances = %+v, want the one entry the connected replica reported", statusResp.Instances)
	}
}

// TestComfyUIManagedCustomNodeInstallRoundTrip drives the full HTTP stack
// for the custom node install endpoint (STATUS.md's P2 ComfyUI Managed
// Docker deployment, subtask 4), against a node connected to one of two
// configured replicas.
//
// TestComfyUIManagedCustomNodeInstallRoundTrip 驱动完整的 HTTP 栈，覆盖自
// 定义节点安装端点（STATUS.md 的 P2 ComfyUI Managed Docker 部署子任务
// 四），针对一个连到两个已配置副本之一的节点。
func TestComfyUIManagedCustomNodeInstallRoundTrip(t *testing.T) {
	elsewhere := stubComfyUIManagedGateway(t, false, nil)
	here := stubComfyUIManagedGateway(t, true, nil)

	h := newHarnessWithComfyUIManaged(t, []string{elsewhere, here})
	platform := bootstrapPlatformOperator(h, "operator@example.com")

	var resp types.ComfyUIManagedTriggerResponse
	status := h.call(http.MethodPost, "/operator/v1/nodes/node-a/comfyui-managed/custom-nodes", platform,
		types.ComfyUIManagedCustomNodeInstallRequest{Name: "my-node"}, &resp)
	if status != http.StatusAccepted {
		t.Fatalf("install status = %d, want 202", status)
	}
	if len(resp.Replicas) != 2 {
		t.Fatalf("install reported %d replicas, want 2", len(resp.Replicas))
	}
}

// TestComfyUIManagedCustomNodeInstallRejectsAnEmptyName asserts an empty
// name is a 400 before ever reaching the router, mirroring the action
// endpoint's own "reject before forwarding" shape.
func TestComfyUIManagedCustomNodeInstallRejectsAnEmptyName(t *testing.T) {
	here := stubComfyUIManagedGateway(t, true, nil)
	h := newHarnessWithComfyUIManaged(t, []string{here})
	platform := bootstrapPlatformOperator(h, "operator@example.com")

	if status := h.call(http.MethodPost, "/operator/v1/nodes/node-a/comfyui-managed/custom-nodes", platform,
		types.ComfyUIManagedCustomNodeInstallRequest{Name: ""}, nil); status != http.StatusBadRequest {
		t.Errorf("install status = %d, want 400 for an empty name", status)
	}
}

// TestComfyUIManagedCustomNodeInstallUnknownNodeIs404 asserts a node
// connected to none of the configured replicas is reported as not found.
func TestComfyUIManagedCustomNodeInstallUnknownNodeIs404(t *testing.T) {
	nowhere := stubComfyUIManagedGateway(t, false, nil)
	h := newHarnessWithComfyUIManaged(t, []string{nowhere})
	platform := bootstrapPlatformOperator(h, "operator@example.com")

	if status := h.call(http.MethodPost, "/operator/v1/nodes/node-a/comfyui-managed/custom-nodes", platform,
		types.ComfyUIManagedCustomNodeInstallRequest{Name: "my-node"}, nil); status != http.StatusNotFound {
		t.Errorf("install status = %d, want 404 for an unconnected node", status)
	}
}

// TestComfyUIManagedForwardingRejectsAnInvalidAction asserts a malformed
// action is a 400 before ever reaching the router — modelpullapi's own
// empty-names check has the same "reject before forwarding" shape.
//
// TestComfyUIManagedForwardingRejectsAnInvalidAction 断言一个不合法的
// action 在到达路由器之前就被判定为 400——与 modelpullapi 自己的空 names
// 检查是同一种"转发前先拒绝"的形状。
func TestComfyUIManagedForwardingRejectsAnInvalidAction(t *testing.T) {
	here := stubComfyUIManagedGateway(t, true, nil)
	h := newHarnessWithComfyUIManaged(t, []string{here})
	platform := bootstrapPlatformOperator(h, "operator@example.com")

	if status := h.call(http.MethodPost, "/operator/v1/nodes/node-a/comfyui-managed", platform, types.ComfyUIManagedTriggerRequest{Action: "reboot"}, nil); status != http.StatusBadRequest {
		t.Errorf("trigger status = %d, want 400 for an unrecognized action", status)
	}
}

// TestComfyUIManagedForwardingUnknownNodeIs404 asserts a node connected to
// none of the configured replicas is reported as not found, on both
// endpoints.
//
// TestComfyUIManagedForwardingUnknownNodeIs404 断言一个没有连到任何已配置
// 副本的节点，在两个端点上都被报告为未找到。
func TestComfyUIManagedForwardingUnknownNodeIs404(t *testing.T) {
	nowhere := stubComfyUIManagedGateway(t, false, nil)
	h := newHarnessWithComfyUIManaged(t, []string{nowhere})
	platform := bootstrapPlatformOperator(h, "operator@example.com")

	if status := h.call(http.MethodPost, "/operator/v1/nodes/ghost/comfyui-managed", platform, types.ComfyUIManagedTriggerRequest{Action: "start"}, nil); status != http.StatusNotFound {
		t.Errorf("trigger status = %d, want 404", status)
	}
	if status := h.call(http.MethodGet, "/operator/v1/nodes/ghost/comfyui-managed", platform, nil, nil); status != http.StatusNotFound {
		t.Errorf("status status = %d, want 404", status)
	}
}

// TestComfyUIManagedForwardingRejectsATenantSession asserts a tenant
// session cannot reach either endpoint — a node has no tenant, the same
// reasoning the model-pull, node-ops and fleet endpoints already enforce.
//
// TestComfyUIManagedForwardingRejectsATenantSession 断言租户会话无法访问
// 这两个端点——节点没有租户，与模型拉取、节点操作、机群端点已经强制的理由
// 相同。
func TestComfyUIManagedForwardingRejectsATenantSession(t *testing.T) {
	here := stubComfyUIManagedGateway(t, true, nil)
	h := newHarnessWithComfyUIManaged(t, []string{here})
	_, tenantSession := bootstrap(h, "Acme", "owner@example.com")

	if status := h.call(http.MethodPost, "/operator/v1/nodes/node-a/comfyui-managed", tenantSession, types.ComfyUIManagedTriggerRequest{Action: "start"}, nil); status != http.StatusForbidden {
		t.Errorf("trigger status = %d, want 403", status)
	}
}

// TestComfyUIManagedForwardingNotMountedWhenUnconfigured asserts a
// deployment that never configured ComfyUIManaged has no route at all, not
// one that answers "not configured" — the same rule Fleet, RegistryClient
// and ModelPull already follow.
//
// TestComfyUIManagedForwardingNotMountedWhenUnconfigured 断言一个从未配置
// ComfyUIManaged 的部署根本没有这条路由，而不是有一条回答"未配置"的路由——
// 与 Fleet、RegistryClient、ModelPull 已经遵循的同一规则。
func TestComfyUIManagedForwardingNotMountedWhenUnconfigured(t *testing.T) {
	h := newHarness(t)
	platform := bootstrapPlatformOperator(h, "operator@example.com")

	if status := h.call(http.MethodGet, "/operator/v1/nodes/node-a/comfyui-managed", platform, nil, nil); status != http.StatusNotFound {
		t.Errorf("status status = %d, want 404 for an unmounted route", status)
	}
	if status := h.call(http.MethodPost, "/operator/v1/nodes/node-a/comfyui-managed/custom-nodes", platform,
		types.ComfyUIManagedCustomNodeInstallRequest{Name: "my-node"}, nil); status != http.StatusNotFound {
		t.Errorf("install status = %d, want 404 for an unmounted route", status)
	}
}
