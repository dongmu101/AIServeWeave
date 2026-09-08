package e2e_test

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

func TestPlatformAuditAuthorizationAndScope(t *testing.T) {
	h := newHarness(t)
	platform := bootstrapPlatformOperator(h, "operator@example.com")
	tenant, tenantSession := bootstrap(h, "Tenant", "tenant@example.com")
	other, _ := bootstrap(h, "Other", "other@example.com")
	for _, entry := range []model.AuditLog{
		{ID: "platform-audit", TenantID: model.PlatformScope, Action: model.ActionNodeApprove},
		{ID: "tenant-audit", TenantID: tenant.Tenant.ID, Action: model.ActionNodeApprove},
		{ID: "other-audit", TenantID: other.Tenant.ID, Action: model.ActionNodeApprove},
	} {
		if err := h.store.AppendAudit(context.Background(), &entry); err != nil {
			t.Fatalf("AppendAudit: got %v, want nil", err)
		}
	}
	for _, tc := range []struct {
		name, path, bearer string
		status             int
		ids                []string
	}{
		{"platform scope", "/operator/v1/audit?tenant_id=" + tenant.Tenant.ID, platform, http.StatusOK, []string{"platform-audit"}},
		{"tenant rejected", "/operator/v1/audit", tenantSession, http.StatusForbidden, nil},
		{"unauthenticated rejected", "/operator/v1/audit", "", http.StatusUnauthorized, nil},
		{"bootstrap rejected", "/operator/v1/audit", bootstrapToken, http.StatusUnauthorized, nil},
		{"tenant isolation", "/admin/v1/audit?tenant_id=" + model.PlatformScope, tenantSession, http.StatusOK, []string{"tenant-audit"}},
		{"platform rejected by tenant audit", "/admin/v1/audit", platform, http.StatusUnauthorized, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			query := u.Query()
			query.Set("action", model.ActionNodeApprove)
			u.RawQuery = query.Encode()
			var page types.AuditListResponse
			status := h.call(http.MethodGet, u.String(), tc.bearer, nil, &page)
			if status != tc.status {
				t.Fatalf("status = %d, want %d", status, tc.status)
			}
			var ids []string
			for _, entry := range page.Items {
				ids = append(ids, entry.ID)
			}
			if !reflect.DeepEqual(ids, tc.ids) {
				t.Fatalf("audit IDs = %v, want %v", ids, tc.ids)
			}
		})
	}
}

func TestPlatformAuditFiltersAndPagination(t *testing.T) {
	h := newHarness(t)
	platform := bootstrapPlatformOperator(h, "operator@example.com")
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, entry := range []model.AuditLog{
		{ID: "first", ActorID: "operator-a", Action: model.ActionNodeApprove, CreatedAt: start},
		{ID: "second", ActorID: "operator-a", Action: model.ActionNodeApprove, CreatedAt: start.Add(time.Hour)},
		{ID: "until-excluded", ActorID: "operator-a", Action: model.ActionNodeApprove, CreatedAt: start.Add(2 * time.Hour)},
		{ID: "actor-excluded", ActorID: "operator-b", Action: model.ActionNodeApprove, CreatedAt: start},
		{ID: "action-excluded", ActorID: "operator-a", Action: model.ActionNodeDisable, CreatedAt: start},
	} {
		entry.TenantID = model.PlatformScope
		if err := h.store.AppendAudit(context.Background(), &entry); err != nil {
			t.Fatalf("AppendAudit: got %v, want nil", err)
		}
	}
	query := url.Values{"action": {model.ActionNodeApprove}, "actor_id": {"operator-a"}, "since": {start.Format(time.RFC3339)}, "until": {start.Add(2 * time.Hour).Format(time.RFC3339)}, "limit": {"1"}}
	for i, id := range []string{"second", "first"} {
		var page types.AuditListResponse
		if status := h.call(http.MethodGet, "/operator/v1/audit?"+query.Encode(), platform, nil, &page); status != http.StatusOK {
			t.Fatalf("page %d status = %d, want %d", i, status, http.StatusOK)
		}
		if len(page.Items) != 1 || page.Items[0].ID != id {
			t.Fatalf("page %d items = %v, want only %s", i, page.Items, id)
		}
		if (page.NextCursor != "") != (i == 0) {
			t.Fatalf("page %d next cursor present = %v, want %v", i, page.NextCursor != "", i == 0)
		}
		query.Set("cursor", page.NextCursor)
	}
	for _, tc := range []struct{ name, query string }{
		{"malformed timestamp", "since=invalid"},
		{"inverted window", "since=2025-01-02T00:00:00Z&until=2025-01-01T00:00:00Z"},
		{"invalid cursor", "cursor=invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if status := h.call(http.MethodGet, "/operator/v1/audit?"+tc.query, platform, nil, nil); status != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
			}
		})
	}
}
