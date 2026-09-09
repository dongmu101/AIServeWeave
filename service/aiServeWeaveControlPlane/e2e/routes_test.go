package e2e_test

import (
	"net/http"
	"strings"
	"testing"

	"AIServeWeave/common/modelroute"
)

func TestRoutePublicationLifecycleAndAuthorization(t *testing.T) {
	h := newHarness(t)
	platform := bootstrapPlatformOperator(h, "routes@example.com")
	_, tenant := bootstrap(h, "Tenant", "route-tenant@example.com")
	for _, tc := range []struct {
		name, bearer string
		want         int
	}{{"platform", platform, 200}, {"tenant", tenant, 403}, {"anonymous", "", 401}, {"internal", internalToken, 401}} {
		t.Run(tc.name, func(t *testing.T) {
			var got modelroute.Snapshot
			status := h.call(http.MethodGet, "/operator/v1/routes", tc.bearer, nil, &got)
			if status != tc.want {
				t.Fatalf("status=%d want %d", status, tc.want)
			}
			if status == 200 && (got.Revision != 0 || got.Routes == nil) {
				t.Fatalf("initial=%+v want zero and empty array", got)
			}
		})
	}
	var got modelroute.Snapshot
	if status := h.call(http.MethodGet, "/internal/v1/routes/current", internalToken, nil, nil); status != 404 {
		t.Fatalf("unpublished status=%d want 404", status)
	}
	routes := []modelroute.Route{{Model: "alias", Targets: []modelroute.Target{{RuntimeModel: "backend", Weight: 3, Priority: 2, NodeSelector: map[string]string{"region": "local"}}}}}
	req := map[string]any{"expected_revision": 0, "routes": routes}
	if status := h.call(http.MethodPost, "/operator/v1/routes/publish", platform, req, &got); status != 201 || got.Revision != 1 {
		t.Fatalf("publish status=%d snapshot=%+v want 201 revision1", status, got)
	}
	if status := h.call(http.MethodPost, "/operator/v1/routes/publish", platform, req, nil); status != 409 {
		t.Fatalf("stale status=%d want409", status)
	}
	if status := h.call(http.MethodPost, "/operator/v1/routes/publish", platform, map[string]any{"expected_revision": 1, "routes": []modelroute.Route{}}, &got); status != 201 || got.Revision != 2 {
		t.Fatalf("empty publish status=%d revision=%d want201/2", status, got.Revision)
	}
	if status := h.call(http.MethodPost, "/operator/v1/routes/rollback", platform, map[string]any{"expected_revision": 2, "revision": 1}, &got); status != 201 || got.Revision != 3 || got.RollbackOf != 1 || len(got.Routes) != 1 {
		t.Fatalf("rollback status=%d snapshot=%+v want201/3/1", status, got)
	}
	if status := h.call(http.MethodGet, "/internal/v1/routes/current", internalToken, nil, &got); status != 200 || got.Revision != 3 {
		t.Fatalf("current status=%d revision=%d want200/3", status, got.Revision)
	}
}

func TestRouteValidationHistoryAndBodyLimits(t *testing.T) {
	h := newHarness(t)
	platform := bootstrapPlatformOperator(h, "route-validation@example.com")
	largeRoutes := []modelroute.Route{{Model: "alias", Targets: []modelroute.Target{{RuntimeModel: "backend", NodeSelector: map[string]string{"long": strings.Repeat("x", 70*1024)}}}}}
	var validated struct {
		Valid  bool   `json:"valid"`
		Digest string `json:"digest"`
	}
	if status := h.call(http.MethodPost, "/operator/v1/routes/validate", platform, map[string]any{"routes": largeRoutes}, &validated); status != 200 || !validated.Valid || validated.Digest == "" {
		t.Fatalf("large validate status=%d value=%+v want200/valid/digest", status, validated)
	}
	for _, tc := range []struct {
		name, path string
		body       any
		want       int
	}{
		{"missing routes", "validate", map[string]any{}, 400},
		{"null routes", "validate", map[string]any{"routes": nil}, 400},
		{"invalid targets", "validate", map[string]any{"routes": []modelroute.Route{{Model: "alias"}}}, 400},
		{"missing expectation", "publish", map[string]any{"routes": []modelroute.Route{}}, 400},
		{"negative expectation", "publish", map[string]any{"expected_revision": -1, "routes": []modelroute.Route{}}, 400},
		{"unknown field", "publish", map[string]any{"expected_revision": 0, "routes": []modelroute.Route{}, "tenant_id": "tenant"}, 400},
		{"oversized document", "validate", map[string]any{"routes": []modelroute.Route{{Model: strings.Repeat("x", modelroute.MaxDocumentBytes), Targets: []modelroute.Target{{RuntimeModel: "m"}}}}}, 413},
		{"missing rollback source", "rollback", map[string]any{"expected_revision": 0, "revision": 1}, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if status := h.call(http.MethodPost, "/operator/v1/routes/"+tc.path, platform, tc.body, nil); status != tc.want {
				t.Fatalf("status=%d want%d", status, tc.want)
			}
		})
	}
	var snapshot modelroute.Snapshot
	for revision := int64(0); revision < 3; revision++ {
		if status := h.call(http.MethodPost, "/operator/v1/routes/publish", platform, map[string]any{"expected_revision": revision, "routes": largeRoutes}, &snapshot); status != 201 {
			t.Fatalf("large publish status=%d want201", status)
		}
	}
	var page struct {
		Items      []modelroute.RevisionInfo `json:"items"`
		NextBefore int64                     `json:"next_before"`
	}
	if status := h.call(http.MethodGet, "/operator/v1/routes/history?limit=2", platform, nil, &page); status != 200 || len(page.Items) != 2 || page.Items[0].Revision != 3 || page.NextBefore != 2 {
		t.Fatalf("page status=%d page=%+v want3,2 next2", status, page)
	}
	page.Items = nil
	page.NextBefore = 0
	if status := h.call(http.MethodGet, "/operator/v1/routes/history?before=2&limit=2", platform, nil, &page); status != 200 || len(page.Items) != 1 || page.Items[0].Revision != 1 || page.NextBefore != 0 {
		t.Fatalf("page status=%d page=%+v want1 no next", status, page)
	}
	if status := h.call(http.MethodGet, "/operator/v1/routes/revisions/1", platform, nil, &snapshot); status != 200 || snapshot.Revision != 1 || snapshot.Digest != validated.Digest {
		t.Fatalf("old snapshot status=%d revision=%d digest=%s want1/%s", status, snapshot.Revision, snapshot.Digest, validated.Digest)
	}
	for _, query := range []string{"limit=0", "limit=51", "limit=no", "before=-1", "before=no"} {
		if status := h.call(http.MethodGet, "/operator/v1/routes/history?"+query, platform, nil, nil); status != 400 {
			t.Fatalf("query=%s status=%d want400", query, status)
		}
	}
}

func TestRouteEndpointCredentialIsolation(t *testing.T) {
	h := newHarness(t)
	platform := bootstrapPlatformOperator(h, "route-isolation@example.com")
	_, tenant := bootstrap(h, "Tenant", "route-isolation-tenant@example.com")
	for _, endpoint := range []struct{ method, path string }{{http.MethodGet, "/operator/v1/routes"}, {http.MethodGet, "/operator/v1/routes/history"}, {http.MethodGet, "/operator/v1/routes/revisions/1"}, {http.MethodGet, "/operator/v1/routes/status"}, {http.MethodPost, "/operator/v1/routes/validate"}, {http.MethodPost, "/operator/v1/routes/publish"}, {http.MethodPost, "/operator/v1/routes/rollback"}} {
		for _, credential := range []struct {
			name, token string
			status      int
		}{{"tenant", tenant, 403}, {"internal", internalToken, 401}, {"bootstrap", bootstrapToken, 401}, {"missing", "", 401}} {
			t.Run(endpoint.path+"/"+credential.name, func(t *testing.T) {
				if got := h.call(endpoint.method, endpoint.path, credential.token, nil, nil); got != credential.status {
					t.Fatalf("status=%d want%d", got, credential.status)
				}
			})
		}
	}
	for _, credential := range []struct{ name, token string }{{"platform", platform}, {"tenant", tenant}, {"bootstrap", bootstrapToken}, {"missing", ""}} {
		t.Run("internal/"+credential.name, func(t *testing.T) {
			if got := h.call(http.MethodGet, "/internal/v1/routes/current", credential.token, nil, nil); got != 401 {
				t.Fatalf("status=%d want401", got)
			}
		})
	}
}
