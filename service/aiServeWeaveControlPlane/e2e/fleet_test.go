package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/workflowview"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/fleet"
)

// stubGateway serves one replica's workflow catalogue and job page, the way a
// real Gateway's operator listener does.
//
// stubGateway 按真实 Gateway 运维监听器的方式，提供一个副本的工作流目录与 job 页。
func stubGateway(t *testing.T, replicaID string, templates []workflowview.Template, jobs []workflowview.Job) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+gatewayToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/internal/v1/workflows":
			_ = json.NewEncoder(w).Encode(workflowview.TemplateCatalog{
				ReplicaID:   replicaID,
				GeneratedAt: time.Now().UTC(),
				Templates:   templates,
			})
		case "/internal/v1/jobs":
			tenantID := r.URL.Query().Get("tenant_id")
			if tenantID == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(workflowview.JobPage{
				ReplicaID:   replicaID,
				GeneratedAt: time.Now().UTC(),
				TenantID:    tenantID,
				Jobs:        jobs,
				Truncated:   true,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// TestTenantWorkflowAndJobViewsCarryNoInfrastructureIdentity is the isolation
// assertion for the two tenant-facing views built on the Gateway read path.
//
// Both are assembled from documents that name replicas and the configured
// endpoints those replicas live at — internal hostnames and ports. A tenant's
// browser is the wrong place for either, by the same reasoning that keeps the
// node inventory off this API entirely. What must survive is what a tenant can
// act on: that the answer may be incomplete.
//
// TestTenantWorkflowAndJobViewsCarryNoInfrastructureIdentity 是对两个建立在 Gateway
// 读取路径之上、面向租户的视图所做的隔离断言。
//
// 两者都由「点名副本、以及那些副本所在的配置 endpoint」的文档组装而成——那是内部主机名
// 与端口。租户的浏览器不是它们该去的地方，理由与把节点清单完全挡在本 API 之外的相同。
// 必须留下来的是租户能据以行动的东西：这个答案可能不完整。
func TestTenantWorkflowAndJobViewsCarryNoInfrastructureIdentity(t *testing.T) {
	gateway := stubGateway(t, "replica-secret-name",
		[]workflowview.Template{{
			ID:     "portrait",
			Valid:  true,
			Inputs: []workflowview.Input{{Name: "prompt", Type: "string", Required: true}},
		}},
		[]workflowview.Job{{
			ID:         "job-1",
			WorkflowID: "portrait",
			State:      "running",
			CreatedAt:  time.Now().UTC(),
			UpdatedAt:  time.Now().UTC(),
		}},
	)

	h := newHarnessWith(t, []string{gateway})
	_, session := bootstrap(h, "Acme", "owner@example.com")

	tests := []struct {
		name string
		path string
	}{
		{name: "the workflow menu", path: "/admin/v1/workflows"},
		{name: "a tenant's runs", path: "/admin/v1/jobs"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw json.RawMessage
			if status := h.call(http.MethodGet, tt.path, session, nil, &raw); status != http.StatusOK {
				t.Fatalf("status = %d, want 200", status)
			}
			body := string(raw)
			for _, leak := range []string{"replica-secret-name", gateway, "127.0.0.1", "endpoint"} {
				if strings.Contains(body, leak) {
					t.Errorf("the response carried %q:\n%s", leak, body)
				}
			}
			if !strings.Contains(body, "partial") {
				t.Errorf("the response dropped the partial flag, which is what a tenant can act on:\n%s", body)
			}
		})
	}
}

// TestTheOperatorSurfaceKeepsTheRollout asserts the same read answers an
// operator with the replicas named. The two surfaces exist because the answer
// differs, and this is the half that would be pointless without them.
//
// TestTheOperatorSurfaceKeepsTheRollout 断言同一次读取在面向运维时会点名副本。两个面
// 之所以存在，正是因为答案不同，而这一半若没有副本信息就毫无意义。
func TestTheOperatorSurfaceKeepsTheRollout(t *testing.T) {
	first := stubGateway(t, "replica-a", []workflowview.Template{
		{ID: "portrait", Valid: true},
		{ID: "upscale", Valid: true},
	}, nil)
	second := stubGateway(t, "replica-b", []workflowview.Template{
		{ID: "portrait", Valid: true},
	}, nil)

	h := newHarnessWith(t, []string{first, second})

	var catalogue fleet.Catalogue
	if status := h.call(http.MethodGet, "/operator/v1/workflows", operatorToken, nil, &catalogue); status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	byID := make(map[string]fleet.Workflow, len(catalogue.Templates))
	for _, workflow := range catalogue.Templates {
		byID[workflow.ID] = workflow
	}
	if agreed := byID["portrait"]; len(agreed.Replicas) != 2 || agreed.Divergent {
		t.Errorf("portrait = %+v, want it on both replicas and not divergent", agreed)
	}
	if partial := byID["upscale"]; !partial.Divergent || len(partial.Replicas) != 1 {
		t.Errorf("upscale = %+v, want it divergent and on one replica", partial)
	}
	if len(catalogue.Replicas) != 2 {
		t.Errorf("got %d replica statuses, want both named for an operator", len(catalogue.Replicas))
	}
}

// TestTheFleetSurfacesRefuseEachOthersCredentials asserts the two secrets are
// not interchangeable. They authorize opposite audiences, and a session that
// could read the fleet — or an operator token that could act as a tenant —
// would make the split decorative.
//
// TestTheFleetSurfacesRefuseEachOthersCredentials 断言那两个密钥不可互换。它们授权的是
// 相反的受众，而一个能读机群的会话——或一个能以租户身份行事的运维 token——会让这道划分
// 沦为装饰。
func TestTheFleetSurfacesRefuseEachOthersCredentials(t *testing.T) {
	gateway := stubGateway(t, "replica-a", []workflowview.Template{{ID: "portrait", Valid: true}}, nil)
	h := newHarnessWith(t, []string{gateway})
	_, session := bootstrap(h, "Acme", "owner@example.com")

	tests := []struct {
		name       string
		path       string
		token      string
		wantStatus int
	}{
		{name: "a session on the tenant menu", path: "/admin/v1/workflows", token: session, wantStatus: http.StatusOK},
		{name: "a session on the operator catalogue", path: "/operator/v1/workflows", token: session, wantStatus: http.StatusUnauthorized},
		{name: "the operator token on the tenant menu", path: "/admin/v1/workflows", token: operatorToken, wantStatus: http.StatusUnauthorized},
		{name: "the operator token on its own catalogue", path: "/operator/v1/workflows", token: operatorToken, wantStatus: http.StatusOK},
		{name: "the gateway token on the operator catalogue", path: "/operator/v1/workflows", token: gatewayToken, wantStatus: http.StatusUnauthorized},
		{name: "the internal token on the operator catalogue", path: "/operator/v1/workflows", token: internalToken, wantStatus: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var discard json.RawMessage
			if status := h.call(http.MethodGet, tt.path, tt.token, nil, &discard); status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
		})
	}
}

// TestTheGatewayReadPathIsAbsentWhenUnconfigured asserts a deployment without
// it has no such routes, rather than routes answering "not configured".
//
// TestTheGatewayReadPathIsAbsentWhenUnconfigured 断言没有配置它的部署根本没有这些路由，
// 而不是有一些回答「未配置」的路由。
func TestTheGatewayReadPathIsAbsentWhenUnconfigured(t *testing.T) {
	h := newHarness(t)
	_, session := bootstrap(h, "Acme", "owner@example.com")

	for _, path := range []string{"/admin/v1/workflows", "/admin/v1/jobs", "/operator/v1/workflows", "/operator/v1/nodes"} {
		var discard json.RawMessage
		status := h.call(http.MethodGet, path, session, nil, &discard)
		if status != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 with no Gateway read path configured", path, status)
		}
	}
}
