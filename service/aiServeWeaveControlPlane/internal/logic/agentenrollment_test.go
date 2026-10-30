package logic_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/agentenrollment"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
)

type enrollmentStub struct {
	calls int
	err   error
}

func (s *enrollmentStub) CA() (agentenrollment.Certificate, error) {
	s.calls++
	return agentenrollment.Certificate{PEM: "public-ca"}, s.err
}
func (s *enrollmentStub) Create(_ context.Context, _, _ string, _ time.Duration) (agentenrollment.Bundle, error) {
	s.calls++
	return agentenrollment.Bundle{NodeID: "new-node", ExpiresAt: time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC), Archive: []byte("test-private-bootstrap-token")}, s.err
}

// TestAgentEnrollmentRequiresPlatformAndAuditsWithoutCredentials checks authorization and audit isolation.
//
// TestAgentEnrollmentRequiresPlatformAndAuditsWithoutCredentials 检查授权与审计隔离。
func TestAgentEnrollmentRequiresPlatformAndAuditsWithoutCredentials(t *testing.T) {
	for _, tc := range []struct {
		name    string
		actor   logic.Actor
		allowed bool
	}{
		{"platform", logic.Actor{UserID: "op_1", TenantID: model.PlatformScope, Role: model.RolePlatformOperator}, true},
		{"tenant owner", logic.Actor{UserID: "usr_1", TenantID: "tenant_1", Role: model.RoleOwner}, false},
		{"tenant admin", logic.Actor{UserID: "usr_1", TenantID: "tenant_1", Role: model.RoleAdmin}, false},
		{"wrong scope", logic.Actor{UserID: "op_1", TenantID: "tenant_1", Role: model.RolePlatformOperator}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := memstore.New()
			source := &enrollmentStub{}
			s := logic.New(st, newFakeClock(), logic.WithAgentEnrollment(source))
			_, readErr := s.AgentEnrollmentCA(tc.actor)
			bundle, err := s.CreateAgentEnrollment(t.Context(), tc.actor, "registry.example.com:9443", "agent.example.com:8443", time.Hour)
			if !tc.allowed {
				if !errors.Is(err, logic.ErrForbidden) || !errors.Is(readErr, logic.ErrForbidden) || source.calls != 0 {
					t.Fatalf("read=%v create=%v calls=%d, want forbidden and zero calls", readErr, err, source.calls)
				}
				return
			}
			if err != nil || readErr != nil || bundle.NodeID != "new-node" {
				t.Fatalf("read=%v create=%v node=%q, want success", readErr, err, bundle.NodeID)
			}
			page, err := st.ListAudit(t.Context(), model.PlatformScope, store.ListQuery{}, store.AuditFilter{})
			if err != nil || len(page.Items) != 1 {
				t.Fatalf("audit count=%d error=%v, want 1", len(page.Items), err)
			}
			audit := page.Items[0]
			if audit.Action != model.ActionNodeEnrollmentCreate || audit.Target != "new-node" || audit.ActorID != "op_1" || strings.Contains(audit.Detail, "test-private-bootstrap-token") {
				t.Fatalf("audit=%+v, want credential-free enrollment audit", audit)
			}
		})
	}
}

// TestAgentEnrollmentFailureDoesNotAuditOrExposeIssuerError checks failed issuance handling.
//
// TestAgentEnrollmentFailureDoesNotAuditOrExposeIssuerError 检查签发失败处理。
func TestAgentEnrollmentFailureDoesNotAuditOrExposeIssuerError(t *testing.T) {
	for _, tc := range []struct {
		name           string
		upstream, want error
	}{
		{"bad input", agentenrollment.ErrInvalidInput, logic.ErrInvalidInput},
		{"issuer error", errors.New("private-admin-token"), logic.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := memstore.New()
			source := &enrollmentStub{err: tc.upstream}
			s := logic.New(st, newFakeClock(), logic.WithAgentEnrollment(source))
			actor := logic.Actor{UserID: "op_1", TenantID: model.PlatformScope, Role: model.RolePlatformOperator}
			_, err := s.CreateAgentEnrollment(t.Context(), actor, "registry.example.com:9443", "agent.example.com:8443", time.Hour)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
			page, err := st.ListAudit(t.Context(), model.PlatformScope, store.ListQuery{}, store.AuditFilter{})
			if err != nil || len(page.Items) != 0 {
				t.Fatalf("audit count=%d error=%v, want zero", len(page.Items), err)
			}
		})
	}
}
