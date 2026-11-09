package logic_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/agentenrollment"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
)

const installTestToken = "test-private-bootstrap-token"

// installIssuerStub mints a fixed token and can be told to fail.
//
// installIssuerStub 签出固定令牌，并可被设为失败。
type installIssuerStub struct {
	mu    sync.Mutex
	mints int
	err   error
}

func (s *installIssuerStub) CA() (agentenrollment.Certificate, error) {
	return agentenrollment.Certificate{PEM: "public-ca", SHA256: "cafe"}, nil
}

func (s *installIssuerStub) MintCredential(_ context.Context, nodeID string, ttl time.Duration) (agentenrollment.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mints++
	if s.err != nil {
		return agentenrollment.Credential{}, s.err
	}
	return agentenrollment.Credential{NodeID: nodeID, Token: installTestToken, ExpiresAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(ttl), CAPEM: "public-ca", CASHA256: "cafe"}, nil
}

type installFixture struct {
	svc    *logic.Service
	st     *memstore.Store
	clock  *fakeClock
	issuer *installIssuerStub
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	st := memstore.New()
	clock := newFakeClock()
	issuer := &installIssuerStub{}
	svc := logic.New(st, clock,
		logic.WithAgentInstall(logic.AgentInstallConfig{Issuer: issuer, PublicURL: "https://open7x.com/"}),
		logic.WithTenantNodes(logic.TenantNodeConfig{RegistryAddr: "registry.example.com:9443", GatewayAddr: "agent.example.com:8443", MaxPerTenant: 2, MaxCreatesPerDay: 5}),
	)
	return &installFixture{svc: svc, st: st, clock: clock, issuer: issuer}
}

var (
	installOperator = logic.Actor{UserID: "op_1", TenantID: model.PlatformScope, Role: model.RolePlatformOperator}
	installOwner    = logic.Actor{UserID: "usr_1", TenantID: "tnt_1", Role: model.RoleOwner}
)

func (f *installFixture) issueOperatorCode(t *testing.T) logic.InstallIssue {
	t.Helper()
	out, err := f.svc.CreateOperatorInstall(t.Context(), installOperator, "registry.example.com:9443", "agent.example.com:8443", logic.InstallCredentialCode, time.Hour)
	if err != nil {
		t.Fatalf("CreateOperatorInstall() error = %v", err)
	}
	return out
}

// TestRedeemInstallCodeSucceedsOnce checks a code redeems exactly once and
// the token is minted only at redemption.
//
// TestRedeemInstallCodeSucceedsOnce 检查一个码只能兑换一次，且令牌只在兑换时签出。
func TestRedeemInstallCodeSucceedsOnce(t *testing.T) {
	f := newInstallFixture(t)
	issued := f.issueOperatorCode(t)
	if f.issuer.mints != 0 || issued.Code == "" || issued.InstallBaseURL != "https://open7x.com" {
		t.Fatalf("issue = %+v mints=%d, want a code, no token minted and a trimmed base URL", issued, f.issuer.mints)
	}
	got, err := f.svc.RedeemInstallCode(t.Context(), strings.ToLower(issued.Code))
	if err != nil || got.NodeID != issued.NodeID || got.BootstrapToken != installTestToken || got.RegistryAddr != "registry.example.com:9443" || got.CASHA256 != "cafe" {
		t.Fatalf("RedeemInstallCode() = %+v, %v; want node %q with token and addresses", got, err, issued.NodeID)
	}
	if _, err := f.svc.RedeemInstallCode(t.Context(), issued.Code); !errors.Is(err, logic.ErrNotFound) {
		t.Fatalf("second RedeemInstallCode() error = %v, want ErrNotFound", err)
	}
}

// TestRedeemInstallCodeRefusals checks every refusal is the same ErrNotFound.
//
// TestRedeemInstallCodeRefusals 检查每种拒绝都是同一个 ErrNotFound。
func TestRedeemInstallCodeRefusals(t *testing.T) {
	f := newInstallFixture(t)
	expired := f.issueOperatorCode(t)
	f.clock.Advance(time.Hour)
	for _, tc := range []struct{ name, code string }{
		{"unknown", "K7QF-9M2D-X4TB-H8RW-3NPC"},
		{"malformed", "not-a-code"},
		{"empty", ""},
		{"expired", expired.Code},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.svc.RedeemInstallCode(t.Context(), tc.code); !errors.Is(err, logic.ErrNotFound) {
				t.Fatalf("RedeemInstallCode(%q) error = %v, want ErrNotFound", tc.code, err)
			}
		})
	}
	if f.issuer.mints != 0 {
		t.Fatalf("mints = %d, want 0 for refused codes", f.issuer.mints)
	}
}

// TestRedeemInstallCodeMintFailureRestoresCode checks a failed mint leaves the
// code usable.
//
// TestRedeemInstallCodeMintFailureRestoresCode 检查签发失败后码仍可使用。
func TestRedeemInstallCodeMintFailureRestoresCode(t *testing.T) {
	f := newInstallFixture(t)
	issued := f.issueOperatorCode(t)
	f.issuer.err = errors.New("registry down")
	if _, err := f.svc.RedeemInstallCode(t.Context(), issued.Code); !errors.Is(err, logic.ErrUnavailable) {
		t.Fatalf("RedeemInstallCode() with failing mint error = %v, want ErrUnavailable", err)
	}
	f.issuer.err = nil
	if got, err := f.svc.RedeemInstallCode(t.Context(), issued.Code); err != nil || got.BootstrapToken != installTestToken {
		t.Fatalf("retry RedeemInstallCode() = %+v, %v; want success after restore", got, err)
	}
}

// TestRedeemInstallCodeConcurrentOnlyOnce checks concurrent redemptions of one
// code succeed once.
//
// TestRedeemInstallCodeConcurrentOnlyOnce 检查同一个码的并发兑换只成功一次。
func TestRedeemInstallCodeConcurrentOnlyOnce(t *testing.T) {
	f := newInstallFixture(t)
	issued := f.issueOperatorCode(t)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.svc.RedeemInstallCode(context.Background(), issued.Code); err == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || f.issuer.mints != 1 {
		t.Fatalf("wins = %d mints = %d, want exactly 1 each", wins.Load(), f.issuer.mints)
	}
}

// TestInstallIssueAuthorizationAndInput checks who may issue and what is
// refused.
//
// TestInstallIssueAuthorizationAndInput 检查谁可以签发以及什么会被拒绝。
func TestInstallIssueAuthorizationAndInput(t *testing.T) {
	f := newInstallFixture(t)
	ctx := t.Context()
	if _, err := f.svc.CreateOperatorInstall(ctx, installOwner, "registry.example.com:9443", "agent.example.com:8443", "code", time.Hour); !errors.Is(err, logic.ErrForbidden) {
		t.Fatalf("tenant owner on operator issue error = %v, want ErrForbidden", err)
	}
	if _, err := f.svc.CreateOperatorInstall(ctx, installOperator, "not an address", "agent.example.com:8443", "code", time.Hour); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatalf("bad address error = %v, want ErrInvalidInput", err)
	}
	if _, err := f.svc.CreateOperatorInstall(ctx, installOperator, "registry.example.com:9443", "agent.example.com:8443", "password", time.Hour); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatalf("unknown credential error = %v, want ErrInvalidInput", err)
	}
	member := logic.Actor{UserID: "usr_2", TenantID: "tnt_1", Role: model.RoleMember}
	if _, err := f.svc.CreateTenantInstall(ctx, member, "code", time.Hour); !errors.Is(err, logic.ErrForbidden) {
		t.Fatalf("member tenant issue error = %v, want ErrForbidden", err)
	}
	if _, err := f.svc.CreateTenantInstall(ctx, installOperator, "code", time.Hour); !errors.Is(err, logic.ErrForbidden) {
		t.Fatalf("platform actor tenant issue error = %v, want ErrForbidden", err)
	}
}

// TestTenantInstallCountsQuotaAndRedeemChecksNode checks a tenant code writes
// the tenant_nodes row, counts against the quota and dies with the node.
//
// TestTenantInstallCountsQuotaAndRedeemChecksNode 检查商户码会写 tenant_nodes 行、计入限额，
// 并随节点删除而失效。
func TestTenantInstallCountsQuotaAndRedeemChecksNode(t *testing.T) {
	f := newInstallFixture(t)
	ctx := t.Context()
	first, err := f.svc.CreateTenantInstall(ctx, installOwner, "code", time.Hour)
	if err != nil {
		t.Fatalf("CreateTenantInstall() error = %v", err)
	}
	second, err := f.svc.CreateTenantInstall(ctx, installOwner, "code", time.Hour)
	if err != nil {
		t.Fatalf("second CreateTenantInstall() error = %v", err)
	}
	if _, err := f.svc.CreateTenantInstall(ctx, installOwner, "code", time.Hour); !errors.Is(err, logic.ErrConflict) {
		t.Fatalf("third CreateTenantInstall() error = %v, want ErrConflict at MaxPerTenant=2", err)
	}
	rows, _ := f.st.ListTenantNodes(ctx, "tnt_1", 10)
	if len(rows) != 2 {
		t.Fatalf("tenant node rows = %d, want 2", len(rows))
	}
	if err := f.st.RevokeTenantNode(ctx, "tnt_1", second.NodeID, "usr_1", f.clock.Now()); err != nil {
		t.Fatalf("RevokeTenantNode() error = %v", err)
	}
	if _, err := f.svc.RedeemInstallCode(ctx, second.Code); !errors.Is(err, logic.ErrNotFound) {
		t.Fatalf("redeem of deleted node's code error = %v, want ErrNotFound", err)
	}
	if got, err := f.svc.RedeemInstallCode(ctx, first.Code); err != nil || got.NodeID != first.NodeID {
		t.Fatalf("redeem of live node's code = %+v, %v; want node %q", got, err, first.NodeID)
	}
}

// TestTenantInstallTokenFailureRemovesRow checks a failed token mint leaves no
// tenant_nodes row behind.
//
// TestTenantInstallTokenFailureRemovesRow 检查令牌签发失败后不留下 tenant_nodes 行。
func TestTenantInstallTokenFailureRemovesRow(t *testing.T) {
	f := newInstallFixture(t)
	f.issuer.err = errors.New("registry down")
	if _, err := f.svc.CreateTenantInstall(t.Context(), installOwner, "token", time.Hour); !errors.Is(err, logic.ErrUnavailable) {
		t.Fatalf("CreateTenantInstall(token) error = %v, want ErrUnavailable", err)
	}
	if rows, _ := f.st.ListTenantNodes(t.Context(), "tnt_1", 10); len(rows) != 0 {
		t.Fatalf("tenant node rows = %d, want 0 after failed issue", len(rows))
	}
}

// TestInstallTokenCredentialAndAuditsHideSecrets checks token mode returns the
// token once and that no audit entry holds a code or token.
//
// TestInstallTokenCredentialAndAuditsHideSecrets 检查令牌模式只返回一次令牌，且任何审计
// 条目都不含兑换码或令牌。
func TestInstallTokenCredentialAndAuditsHideSecrets(t *testing.T) {
	f := newInstallFixture(t)
	ctx := t.Context()
	tokenIssue, err := f.svc.CreateOperatorInstall(ctx, installOperator, "registry.example.com:9443", "agent.example.com:8443", logic.InstallCredentialToken, time.Hour)
	if err != nil || tokenIssue.BootstrapToken != installTestToken || tokenIssue.Code != "" || tokenIssue.CASHA256 != "cafe" {
		t.Fatalf("token issue = %+v, %v; want token, ca hash and no code", tokenIssue, err)
	}
	codeIssue := f.issueOperatorCode(t)
	if _, err := f.svc.RedeemInstallCode(ctx, codeIssue.Code); err != nil {
		t.Fatalf("RedeemInstallCode() error = %v", err)
	}
	page, err := f.st.ListAudit(ctx, model.PlatformScope, store.ListQuery{}, store.AuditFilter{})
	if err != nil || len(page.Items) != 5 {
		t.Fatalf("audit entries = %d, %v; want 5 (token issue, code issue, redeem and sharing settings)", len(page.Items), err)
	}
	for _, entry := range page.Items {
		for _, secret := range []string{installTestToken, codeIssue.Code, strings.ReplaceAll(codeIssue.Code, "-", "")} {
			if strings.Contains(entry.Detail+entry.Target, secret) {
				t.Fatalf("audit %s leaks %q: %+v", entry.Action, secret, entry)
			}
		}
	}
}

// TestInstallDisabledIsNotFound checks the feature is invisible when off.
//
// TestInstallDisabledIsNotFound 检查功能关闭时不可见。
func TestInstallDisabledIsNotFound(t *testing.T) {
	s := logic.New(memstore.New(), newFakeClock())
	if _, err := s.RedeemInstallCode(t.Context(), "K7QF-9M2D-X4TB-H8RW-3NPC"); !errors.Is(err, logic.ErrNotFound) {
		t.Fatalf("RedeemInstallCode() error = %v, want ErrNotFound", err)
	}
	if _, err := s.InstallCA(); !errors.Is(err, logic.ErrNotFound) {
		t.Fatalf("InstallCA() error = %v, want ErrNotFound", err)
	}
	if _, err := s.CreateOperatorInstall(t.Context(), installOperator, "a.example.com:1", "b.example.com:2", "code", time.Hour); !errors.Is(err, logic.ErrNotFound) {
		t.Fatalf("CreateOperatorInstall() error = %v, want ErrNotFound", err)
	}
}

func TestOperatorInstallPersistsExplicitSharingBeforeReturningCredential(t *testing.T) {
	for _, shared := range []bool{false, true} {
		f := newInstallFixture(t)
		issued, err := f.svc.CreateOperatorInstall(t.Context(), installOperator, "registry.example.com:9443", "agent.example.com:8443", logic.InstallCredentialCode, time.Hour, shared)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := f.svc.ListNodeAccess(t.Context(), installOperator)
		if err != nil || len(rows) != 1 || rows[0].NodeID != issued.NodeID {
			t.Fatal(rows, err)
		}
		want := "private"
		if shared {
			want = "shared"
		}
		if rows[0].Mode != want {
			t.Fatalf("install sharing mode=%s, want %s", rows[0].Mode, want)
		}
	}
}
