package e2e_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"AIServeWeave/common/workflowtemplate"
	"AIServeWeave/common/workflowview"
)

const wtGraph = `{"6":{"class_type":"CLIPTextEncode","inputs":{"text":"a cat"}}}`

func wtContent(description string) workflowtemplate.Content {
	return workflowtemplate.Content{
		Description: description,
		Inputs:      []workflowtemplate.Input{{Name: "prompt", Node: "6", Field: "text", Type: workflowtemplate.InputString}},
		Graph:       json.RawMessage(wtGraph),
	}
}

func TestWorkflowTemplatePublicationLifecycleAndAuthorization(t *testing.T) {
	h := newHarness(t)
	platform := bootstrapPlatformOperator(h, "workflow-templates@example.com")
	_, tenant := bootstrap(h, "Tenant", "workflow-templates-tenant@example.com")
	for _, tc := range []struct {
		name, bearer string
		want         int
	}{{"platform", platform, 200}, {"tenant", tenant, 403}, {"anonymous", "", 401}, {"internal", internalToken, 401}} {
		t.Run(tc.name, func(t *testing.T) {
			if status := h.call(http.MethodGet, "/operator/v1/workflow-templates", tc.bearer, nil, nil); status != tc.want {
				t.Fatalf("status=%d want %d", status, tc.want)
			}
		})
	}
	if status := h.call(http.MethodGet, "/operator/v1/workflow-templates/alpha", platform, nil, nil); status != 404 {
		t.Fatalf("unpublished status=%d want 404", status)
	}

	var got workflowtemplate.Snapshot
	publish := map[string]any{"expected_revision": 0, "content": wtContent("v1"), "visible_tenant_ids": []string{}}
	if status := h.call(http.MethodPost, "/operator/v1/workflow-templates/alpha/publish", platform, publish, &got); status != 201 || got.Revision != 1 || got.TemplateID != "alpha" {
		t.Fatalf("publish status=%d snapshot=%+v want 201 revision 1", status, got)
	}
	if status := h.call(http.MethodPost, "/operator/v1/workflow-templates/alpha/publish", platform, publish, nil); status != 409 {
		t.Fatalf("stale status=%d want 409", status)
	}
	publish2 := map[string]any{"expected_revision": 1, "content": wtContent("v2"), "visible_tenant_ids": []string{"tenant-x"}}
	if status := h.call(http.MethodPost, "/operator/v1/workflow-templates/alpha/publish", platform, publish2, &got); status != 201 || got.Revision != 2 || got.Description != "v2" || len(got.VisibleTenantIDs) != 1 {
		t.Fatalf("republish status=%d snapshot=%+v want 201 revision 2", status, got)
	}
	if status := h.call(http.MethodPost, "/operator/v1/workflow-templates/alpha/rollback", platform, map[string]any{"expected_revision": 2, "revision": 1}, &got); status != 201 || got.Revision != 3 || got.RollbackOf != 1 || got.Description != "v1" {
		t.Fatalf("rollback status=%d snapshot=%+v want 201/3/rollback_of=1", status, got)
	}
	if status := h.call(http.MethodGet, "/operator/v1/workflow-templates/alpha", platform, nil, &got); status != 200 || got.Revision != 3 {
		t.Fatalf("current status=%d revision=%d want 200/3", status, got.Revision)
	}

	var bundle []workflowtemplate.Snapshot
	if status := h.call(http.MethodGet, "/internal/v1/workflow-templates/current", internalToken, nil, &bundle); status != 200 || len(bundle) != 1 || bundle[0].Revision != 3 {
		t.Fatalf("bundle status=%d bundle=%+v want 200 one snapshot at revision 3", status, bundle)
	}

	var list struct {
		Items []struct {
			TemplateID string `json:"template_id"`
			Revision   int64  `json:"revision"`
		} `json:"items"`
	}
	if status := h.call(http.MethodGet, "/operator/v1/workflow-templates", platform, nil, &list); status != 200 || len(list.Items) != 1 || list.Items[0].TemplateID != "alpha" || list.Items[0].Revision != 3 {
		t.Fatalf("list status=%d list=%+v want one alpha at revision 3", status, list)
	}
}

func TestWorkflowTemplateSecondTemplateIsIndependentlyVersioned(t *testing.T) {
	h := newHarness(t)
	platform := bootstrapPlatformOperator(h, "workflow-templates-second@example.com")
	var got workflowtemplate.Snapshot
	if status := h.call(http.MethodPost, "/operator/v1/workflow-templates/alpha/publish", platform, map[string]any{"expected_revision": 0, "content": wtContent("a")}, &got); status != 201 || got.Revision != 1 {
		t.Fatalf("alpha publish status=%d snapshot=%+v want 201/1", status, got)
	}
	if status := h.call(http.MethodPost, "/operator/v1/workflow-templates/alpha/publish", platform, map[string]any{"expected_revision": 1, "content": wtContent("a2")}, &got); status != 201 || got.Revision != 2 {
		t.Fatalf("alpha republish status=%d snapshot=%+v want 201/2", status, got)
	}
	if status := h.call(http.MethodPost, "/operator/v1/workflow-templates/beta/publish", platform, map[string]any{"expected_revision": 0, "content": wtContent("b")}, &got); status != 201 || got.Revision != 1 {
		t.Fatalf("beta publish status=%d snapshot=%+v want 201/1, independent from alpha's own counter", status, got)
	}
}

func TestWorkflowTemplateValidationHistoryAndBodyLimits(t *testing.T) {
	h := newHarness(t)
	platform := bootstrapPlatformOperator(h, "workflow-templates-validation@example.com")
	var validated struct {
		Valid  bool   `json:"valid"`
		Digest string `json:"digest"`
	}
	if status := h.call(http.MethodPost, "/operator/v1/workflow-templates/alpha/validate", platform, map[string]any{"content": wtContent("v")}, &validated); status != 200 || !validated.Valid || validated.Digest == "" {
		t.Fatalf("validate status=%d value=%+v want 200/valid/digest", status, validated)
	}
	for _, tc := range []struct {
		name, path string
		body       any
		want       int
	}{
		{"invalid graph", "validate", map[string]any{"content": workflowtemplate.Content{Graph: json.RawMessage(`"nope"`)}}, 400},
		{"missing expectation", "publish", map[string]any{"content": wtContent("v")}, 400},
		{"negative expectation", "publish", map[string]any{"expected_revision": -1, "content": wtContent("v")}, 400},
		{"unknown field", "publish", map[string]any{"expected_revision": 0, "content": wtContent("v"), "tenant_id": "tenant"}, 400},
		{"missing rollback source", "rollback", map[string]any{"expected_revision": 0, "revision": 1}, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if status := h.call(http.MethodPost, "/operator/v1/workflow-templates/alpha/"+tc.path, platform, tc.body, nil); status != tc.want {
				t.Fatalf("status=%d want %d", status, tc.want)
			}
		})
	}
	oversizedContent := wtContent(strings.Repeat("x", workflowtemplate.MaxContentBytes))
	if status := h.call(http.MethodPost, "/operator/v1/workflow-templates/alpha/validate", platform, map[string]any{"content": oversizedContent}, nil); status != 413 {
		t.Fatalf("oversized status=%d want 413", status)
	}

	var snapshot workflowtemplate.Snapshot
	for revision := int64(0); revision < 3; revision++ {
		if status := h.call(http.MethodPost, "/operator/v1/workflow-templates/alpha/publish", platform, map[string]any{"expected_revision": revision, "content": wtContent("v")}, &snapshot); status != 201 {
			t.Fatalf("publish status=%d want 201", status)
		}
	}
	var page struct {
		Items      []workflowtemplate.RevisionInfo `json:"items"`
		NextBefore int64                           `json:"next_before"`
	}
	if status := h.call(http.MethodGet, "/operator/v1/workflow-templates/alpha/history?limit=2", platform, nil, &page); status != 200 || len(page.Items) != 2 || page.Items[0].Revision != 3 || page.NextBefore != 2 {
		t.Fatalf("page status=%d page=%+v want 3,2 next 2", status, page)
	}
	page.Items = nil
	page.NextBefore = 0
	if status := h.call(http.MethodGet, "/operator/v1/workflow-templates/alpha/history?before=2&limit=2", platform, nil, &page); status != 200 || len(page.Items) != 1 || page.Items[0].Revision != 1 || page.NextBefore != 0 {
		t.Fatalf("page status=%d page=%+v want 1 no next", status, page)
	}
	if status := h.call(http.MethodGet, "/operator/v1/workflow-templates/alpha/revisions/1", platform, nil, &snapshot); status != 200 || snapshot.Revision != 1 || snapshot.Digest != validated.Digest {
		t.Fatalf("old snapshot status=%d revision=%d digest=%s want 1/%s", status, snapshot.Revision, snapshot.Digest, validated.Digest)
	}
	for _, query := range []string{"limit=0", "limit=51", "limit=no", "before=-1", "before=no"} {
		if status := h.call(http.MethodGet, "/operator/v1/workflow-templates/alpha/history?"+query, platform, nil, nil); status != 400 {
			t.Fatalf("query=%s status=%d want 400", query, status)
		}
	}
}

// TestWorkflowTemplateStatusRouteIsNotShadowedByTheParameterizedGetRoute
// guards the same class of routing bug jobs' /internal/v1/jobs/active does
// for /internal/v1/jobs/:id: "status" is a literal sibling segment sitting
// where :id also matches, and it must never be swallowed by the templateID
// wildcard.
//
// TestWorkflowTemplateStatusRouteIsNotShadowedByTheParameterizedGetRoute
// 守护的是与 jobs 的 /internal/v1/jobs/active 之于 /internal/v1/jobs/:id
// 同一类路由缺陷:"status" 是一个恰好落在 :id 也能匹配的位置上的字面量兄弟路径，
// 绝不能被 templateID 通配符吞掉。
func TestWorkflowTemplateStatusRouteIsNotShadowedByTheParameterizedGetRoute(t *testing.T) {
	h := newHarness(t)
	platform := bootstrapPlatformOperator(h, "workflow-templates-status@example.com")
	var report struct {
		DesiredTemplateCount int  `json:"desired_template_count"`
		Complete             bool `json:"complete"`
	}
	if status := h.call(http.MethodGet, "/operator/v1/workflow-templates/status", platform, nil, &report); status != 200 {
		t.Fatalf("status endpoint status=%d want 200 (not swallowed by :id)", status)
	}
	if status := h.call(http.MethodGet, "/operator/v1/workflow-templates/actually-missing", platform, nil, nil); status != 404 {
		t.Fatalf("nonexistent id status=%d want 404", status)
	}
}

func TestWorkflowTemplateEndpointCredentialIsolation(t *testing.T) {
	h := newHarness(t)
	platform := bootstrapPlatformOperator(h, "workflow-templates-isolation@example.com")
	_, tenant := bootstrap(h, "Tenant", "workflow-templates-isolation-tenant@example.com")
	if status := h.call(http.MethodPost, "/operator/v1/workflow-templates/alpha/publish", platform, map[string]any{"expected_revision": 0, "content": wtContent("v")}, nil); status != 201 {
		t.Fatalf("seed publish status=%d want 201", status)
	}
	for _, endpoint := range []struct{ method, path string }{
		{http.MethodGet, "/operator/v1/workflow-templates"},
		{http.MethodGet, "/operator/v1/workflow-templates/alpha"},
		{http.MethodGet, "/operator/v1/workflow-templates/alpha/history"},
		{http.MethodGet, "/operator/v1/workflow-templates/alpha/revisions/1"},
		{http.MethodGet, "/operator/v1/workflow-templates/status"},
		{http.MethodPost, "/operator/v1/workflow-templates/alpha/validate"},
		{http.MethodPost, "/operator/v1/workflow-templates/alpha/publish"},
		{http.MethodPost, "/operator/v1/workflow-templates/alpha/rollback"},
	} {
		for _, credential := range []struct {
			name, token string
			status      int
		}{{"tenant", tenant, 403}, {"internal", internalToken, 401}, {"bootstrap", bootstrapToken, 401}, {"missing", "", 401}} {
			t.Run(endpoint.path+"/"+credential.name, func(t *testing.T) {
				if got := h.call(endpoint.method, endpoint.path, credential.token, nil, nil); got != credential.status {
					t.Fatalf("status=%d want %d", got, credential.status)
				}
			})
		}
	}
	for _, credential := range []struct{ name, token string }{{"platform", platform}, {"tenant", tenant}, {"bootstrap", bootstrapToken}, {"missing", ""}} {
		t.Run("internal/"+credential.name, func(t *testing.T) {
			if got := h.call(http.MethodGet, "/internal/v1/workflow-templates/current", credential.token, nil, nil); got != 401 {
				t.Fatalf("status=%d want 401", got)
			}
		})
	}
}

// TestWorkflowTemplateTenantVisibilityFiltersTheTenantMenu exercises the
// filtering listWorkflows applies (P03), which lives entirely on the
// Fleet-aggregated tenant menu — /admin/v1/workflows renders whatever a
// connected Gateway replica reports having loaded, not what this service's
// own workflow-template store holds (that store reaches a Gateway only via
// its separate controlplane sync path). So this test drives a stub Gateway
// the way TestTenantWorkflowAndJobViewsCarryNoInfrastructureIdentity in
// fleet_test.go does, rather than publishing through
// /operator/v1/workflow-templates.
//
// TestWorkflowTemplateTenantVisibilityFiltersTheTenantMenu 演练
// listWorkflows 施加的过滤（P03），它完全落在 Fleet 聚合出的租户菜单上——
// /admin/v1/workflows 渲染的是某个已连接 Gateway 副本报告自己加载了什么，而不是
// 本服务自己工作流模板存储所持有的东西（那份存储只经由其独立的 controlplane
// 同步路径才能抵达某个 Gateway）。因此本测试像 fleet_test.go 里的
// TestTenantWorkflowAndJobViewsCarryNoInfrastructureIdentity 那样驱动一个桩
// Gateway，而不是经 /operator/v1/workflow-templates 发布。
func TestWorkflowTemplateTenantVisibilityFiltersTheTenantMenu(t *testing.T) {
	h := newHarness(t)
	_, allowedTenant := bootstrap(h, "Allowed", "workflow-templates-allowed@example.com")
	created, otherTenant := bootstrap(h, "Other", "workflow-templates-other@example.com")
	_ = created

	allowedTenantID := allowedTenantIDFor(t, h, allowedTenant)

	gateway := stubGateway(t, "replica-a", []workflowview.Template{
		{ID: "public", Valid: true, Inputs: []workflowview.Input{}},
		{ID: "private", Valid: true, Inputs: []workflowview.Input{}, VisibleTenantIDs: []string{allowedTenantID}},
	}, nil)
	h2 := newHarnessWith(t, []string{gateway})

	var forAllowed struct {
		Templates []workflowview.Template `json:"templates"`
	}
	if status := h2.call(http.MethodGet, "/admin/v1/workflows", allowedTenant, nil, &forAllowed); status != 200 {
		t.Fatalf("allowed tenant menu status=%d want 200", status)
	}
	if !containsWorkflowID(forAllowed.Templates, "public") || !containsWorkflowID(forAllowed.Templates, "private") {
		t.Fatalf("allowed tenant menu=%+v want both public and private", forAllowed)
	}

	var forOther struct {
		Templates []workflowview.Template `json:"templates"`
	}
	if status := h2.call(http.MethodGet, "/admin/v1/workflows", otherTenant, nil, &forOther); status != 200 {
		t.Fatalf("other tenant menu status=%d want 200", status)
	}
	if !containsWorkflowID(forOther.Templates, "public") || containsWorkflowID(forOther.Templates, "private") {
		t.Fatalf("other tenant menu=%+v want public only, private must be filtered out", forOther)
	}
}

// allowedTenantIDFor reads back the tenant id a session belongs to, via the
// same self-service endpoint the Console uses to render a tenant's own
// details.
//
// allowedTenantIDFor 通过 Console 用来渲染租户自身详情的同一个自助端点，读回
// 一个会话所属的租户 id。
func allowedTenantIDFor(t *testing.T, h *harness, session string) string {
	t.Helper()
	var current struct {
		Tenant struct {
			ID string `json:"id"`
		} `json:"tenant"`
	}
	if status := h.call(http.MethodGet, "/admin/v1/tenants/current", session, nil, &current); status != 200 || current.Tenant.ID == "" {
		t.Fatalf("reading current tenant: status=%d id=%q", status, current.Tenant.ID)
	}
	return current.Tenant.ID
}

func containsWorkflowID(templates []workflowview.Template, id string) bool {
	for _, tpl := range templates {
		if tpl.ID == id {
			return true
		}
	}
	return false
}
