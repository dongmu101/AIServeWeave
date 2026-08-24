package e2e_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"AIServeWeave/common/apikey"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
	"AIServeWeave/service/aiServeWeaveGateway/controlplaneclient"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

// bootstrap creates a tenant and signs its owner in, returning the session
// token every later call uses.
//
// bootstrap 创建一个租户并让它的 owner 登录，返回之后每次调用都要用的会话令牌。
func bootstrap(h *harness, name, email string) (types.CreateTenantResponse, string) {
	h.t.Helper()

	var created types.CreateTenantResponse
	status := h.call(http.MethodPost, "/admin/v1/tenants", bootstrapToken, types.CreateTenantRequest{
		Name:          name,
		OwnerEmail:    email,
		OwnerPassword: ownerPassword,
	}, &created)
	if status != http.StatusCreated {
		h.t.Fatalf("creating a tenant: status %d", status)
	}

	var session types.LoginResponse
	status = h.call(http.MethodPost, "/admin/v1/auth/login", "", types.LoginRequest{
		Email:    email,
		Password: ownerPassword,
	}, &session)
	if status != http.StatusOK {
		h.t.Fatalf("signing in: status %d", status)
	}
	return created, session.Token
}

// gatewayVerifier returns the Gateway's real verification client, pointed at
// this control plane.
//
// gatewayVerifier 返回 Gateway 真实的校验客户端，指向本控制面。
func gatewayVerifier(h *harness, clock *steppableClock) *controlplaneclient.Verifier {
	h.t.Helper()
	verifier, err := controlplaneclient.New(controlplaneclient.Config{
		Endpoint: h.base,
		Token:    internalToken,
		Clock:    clock,
	})
	if err != nil {
		h.t.Fatalf("controlplaneclient.New: %v", err)
	}
	return verifier
}

// TestKeyIssuedByTheConsoleAuthenticatesAtTheGateway is the closed loop this
// whole change exists for: a key minted through the Admin API authenticates
// through the Gateway's own verification path, and reports the tenant that
// minted it.
//
// TestKeyIssuedByTheConsoleAuthenticatesAtTheGateway 就是本次改动存在的意义所在的那个
// 闭环：一个经由 Admin API 铸造的 key，能通过 Gateway 自己的校验路径完成认证，并报出
// 铸造它的那个租户。
func TestKeyIssuedByTheConsoleAuthenticatesAtTheGateway(t *testing.T) {
	h := newHarness(t)
	created, session := bootstrap(h, "Acme", "owner@example.com")

	var key types.CreateAPIKeyResponse
	if status := h.call(http.MethodPost, "/admin/v1/apikeys", session,
		types.CreateAPIKeyRequest{Name: "production"}, &key); status != http.StatusCreated {
		t.Fatalf("creating a key: status %d", status)
	}
	if key.Key == "" {
		t.Fatal("the create response carried no plaintext key")
	}

	verifier := gatewayVerifier(h, newSteppableClock())
	identity, err := verifier.Verify(context.Background(), key.Key)
	if err != nil {
		t.Fatalf("the Gateway could not verify a freshly issued key: %v", err)
	}
	if identity.TenantID != created.Tenant.ID {
		t.Errorf("TenantID = %q, want %q", identity.TenantID, created.Tenant.ID)
	}
	if identity.KeyID != key.APIKey.ID {
		t.Errorf("KeyID = %q, want %q", identity.KeyID, key.APIKey.ID)
	}
}

// TestRevokedKeyStopsWorkingAtTheGateway covers the other half of the loop,
// including the cache window: revocation takes effect at the control plane
// immediately, and at the Gateway once its in-process cache entry expires.
// That window is a documented cost, and this test is where its size is pinned.
//
// TestRevokedKeyStopsWorkingAtTheGateway 覆盖这个闭环的另一半，包括缓存窗口：吊销在
// 控制面立即生效，在 Gateway 则要等它进程内的缓存条目过期。那个窗口是一项有文档记载的
// 代价，而它的大小正是由本测试钉住的。
func TestRevokedKeyStopsWorkingAtTheGateway(t *testing.T) {
	h := newHarness(t)
	_, session := bootstrap(h, "Acme", "owner@example.com")

	var key types.CreateAPIKeyResponse
	if status := h.call(http.MethodPost, "/admin/v1/apikeys", session,
		types.CreateAPIKeyRequest{Name: "leaked"}, &key); status != http.StatusCreated {
		t.Fatalf("creating a key: status %d", status)
	}

	clock := newSteppableClock()
	verifier := gatewayVerifier(h, clock)
	if _, err := verifier.Verify(context.Background(), key.Key); err != nil {
		t.Fatalf("the key did not work before revocation: %v", err)
	}

	if status := h.call(http.MethodDelete, "/admin/v1/apikeys/"+key.APIKey.ID, session, nil, nil); status != http.StatusNoContent {
		t.Fatalf("revoking the key: status %d", status)
	}

	// A fresh verifier has no cache entry, so it sees the revocation at once —
	// this is what the control plane itself now answers.
	//
	// 新建的 verifier 没有缓存条目，因此它立刻就能看到吊销结果——这正是控制面此刻给出
	// 的答复。
	fresh := gatewayVerifier(h, newSteppableClock())
	if _, err := fresh.Verify(context.Background(), key.Key); !errors.Is(err, httpapi.ErrKeyRejected) {
		t.Errorf("the control plane still accepts a revoked key: err = %v", err)
	}

	// The original verifier keeps serving it until its cached entry expires.
	//
	// 原来那个 verifier 会继续放行它，直到其缓存条目过期。
	if _, err := verifier.Verify(context.Background(), key.Key); err != nil {
		t.Errorf("the cached entry stopped working early, which contradicts the documented window: %v", err)
	}
	clock.Advance(controlplaneclient.DefaultCacheTTL + time.Second)
	if _, err := verifier.Verify(context.Background(), key.Key); !errors.Is(err, httpapi.ErrKeyRejected) {
		t.Errorf("a revoked key outlived the cache TTL: err = %v", err)
	}
}

// TestAdminAPIRefusesUnauthenticatedCallers walks every guarded route without
// credentials. It is the assertion that the guard groups in routes.go are
// actually applied, which is a routing mistake no unit test would catch.
//
// TestAdminAPIRefusesUnauthenticatedCallers 在不带凭据的情况下走一遍每条受保护的路由。
// 它断言 routes.go 里的那些守卫组确实生效了——那是一类单元测试抓不到的路由错误。
func TestAdminAPIRefusesUnauthenticatedCallers(t *testing.T) {
	h := newHarness(t)
	_, session := bootstrap(h, "Acme", "owner@example.com")

	tests := []struct {
		name   string
		method string
		path   string
		bearer string
		body   any
	}{
		{name: "list keys with no token", method: http.MethodGet, path: "/admin/v1/apikeys"},
		{name: "list keys with a junk token", method: http.MethodGet, path: "/admin/v1/apikeys", bearer: "not-a-jwt"},
		{name: "list users with no token", method: http.MethodGet, path: "/admin/v1/users"},
		{name: "read audit with no token", method: http.MethodGet, path: "/admin/v1/audit"},
		{
			name: "create a key with no token", method: http.MethodPost, path: "/admin/v1/apikeys",
			body: types.CreateAPIKeyRequest{Name: "x"},
		},
		{
			name:   "create a tenant with a session token instead of the bootstrap token",
			method: http.MethodPost, path: "/admin/v1/tenants", bearer: session,
			body: types.CreateTenantRequest{Name: "B", OwnerEmail: "b@example.com", OwnerPassword: ownerPassword},
		},
		{
			name:   "verify a key with a session token instead of the internal token",
			method: http.MethodPost, path: "/internal/v1/apikeys/verify", bearer: session,
			body: types.VerifyRequest{Hash: apikey.Hash("aisw-whatever")},
		},
		{
			name:   "verify a key with the bootstrap token instead of the internal one",
			method: http.MethodPost, path: "/internal/v1/apikeys/verify", bearer: bootstrapToken,
			body: types.VerifyRequest{Hash: apikey.Hash("aisw-whatever")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if status := h.call(tt.method, tt.path, tt.bearer, tt.body, nil); status != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", status)
			}
		})
	}
}

// TestTenantsAreIsolatedOverHTTP asserts the isolation holds through the real
// request path, not only in the logic layer's own tests: one tenant's session
// must not see or revoke another's keys.
//
// TestTenantsAreIsolatedOverHTTP 断言隔离在真实请求路径上同样成立，而不只是在 logic 层
// 自己的测试里成立：一个租户的会话不得看到或吊销另一个租户的 key。
func TestTenantsAreIsolatedOverHTTP(t *testing.T) {
	h := newHarness(t)
	_, sessionA := bootstrap(h, "Acme", "a@example.com")
	_, sessionB := bootstrap(h, "Beta", "b@example.com")

	var keyA types.CreateAPIKeyResponse
	if status := h.call(http.MethodPost, "/admin/v1/apikeys", sessionA,
		types.CreateAPIKeyRequest{Name: "acme"}, &keyA); status != http.StatusCreated {
		t.Fatalf("creating a key for tenant A: status %d", status)
	}

	var listedByB []types.APIKey
	if status := h.call(http.MethodGet, "/admin/v1/apikeys", sessionB, nil, &listedByB); status != http.StatusOK {
		t.Fatalf("listing keys as tenant B: status %d", status)
	}
	for _, key := range listedByB {
		if key.ID == keyA.APIKey.ID {
			t.Errorf("tenant B can see tenant A's key %q", key.ID)
		}
	}

	if status := h.call(http.MethodDelete, "/admin/v1/apikeys/"+keyA.APIKey.ID, sessionB, nil, nil); status != http.StatusNotFound {
		t.Errorf("tenant B revoking tenant A's key: status = %d, want 404", status)
	}

	verifier := gatewayVerifier(h, newSteppableClock())
	if _, err := verifier.Verify(context.Background(), keyA.Key); err != nil {
		t.Errorf("tenant A's key stopped working after tenant B tried to revoke it: %v", err)
	}
}

// TestListedKeysNeverCarryTheSecret asserts over the wire what the logic tests
// assert in memory: a listing identifies keys and cannot reproduce one.
//
// TestListedKeysNeverCarryTheSecret 在线上验证 logic 测试在内存中断言过的事：列表可以
// 标识各个 key，却无法重现出其中任何一个。
func TestListedKeysNeverCarryTheSecret(t *testing.T) {
	h := newHarness(t)
	_, session := bootstrap(h, "Acme", "owner@example.com")

	var created types.CreateAPIKeyResponse
	if status := h.call(http.MethodPost, "/admin/v1/apikeys", session,
		types.CreateAPIKeyRequest{Name: "production"}, &created); status != http.StatusCreated {
		t.Fatalf("creating a key: status %d", status)
	}

	var listed []types.APIKey
	if status := h.call(http.MethodGet, "/admin/v1/apikeys", session, nil, &listed); status != http.StatusOK {
		t.Fatalf("listing keys: status %d", status)
	}
	if len(listed) != 1 {
		t.Fatalf("got %d keys, want 1", len(listed))
	}
	if listed[0].Display != created.APIKey.Display {
		t.Errorf("Display = %q, want %q", listed[0].Display, created.APIKey.Display)
	}
	// The wire type has no field for either the plaintext or the hash, so the
	// assertion is that the listing cannot be turned back into a working key.
	//
	// 线上类型既没有明文字段也没有哈希字段，因此这里断言的是：列表无法被还原成一个
	// 能用的 key。
	verifier := gatewayVerifier(h, newSteppableClock())
	if _, err := verifier.Verify(context.Background(), listed[0].Display); !errors.Is(err, httpapi.ErrKeyRejected) {
		t.Errorf("the display form authenticated as a key: err = %v", err)
	}
}

// TestAdministrativeActionsAreAudited asserts the trail is readable through
// the API and names the actions that were taken.
//
// TestAdministrativeActionsAreAudited 断言审计线索可以通过 API 读取，并点出所发生的
// 那些动作。
func TestAdministrativeActionsAreAudited(t *testing.T) {
	h := newHarness(t)
	_, session := bootstrap(h, "Acme", "owner@example.com")

	var key types.CreateAPIKeyResponse
	if status := h.call(http.MethodPost, "/admin/v1/apikeys", session,
		types.CreateAPIKeyRequest{Name: "production"}, &key); status != http.StatusCreated {
		t.Fatalf("creating a key: status %d", status)
	}
	if status := h.call(http.MethodDelete, "/admin/v1/apikeys/"+key.APIKey.ID, session, nil, nil); status != http.StatusNoContent {
		t.Fatalf("revoking the key: status %d", status)
	}

	var entries []types.AuditEntry
	if status := h.call(http.MethodGet, "/admin/v1/audit", session, nil, &entries); status != http.StatusOK {
		t.Fatalf("reading the audit trail: status %d", status)
	}

	want := map[string]bool{
		"tenant.create": false,
		"user.login":    false,
		"apikey.create": false,
		"apikey.revoke": false,
	}
	for _, entry := range entries {
		if _, tracked := want[entry.Action]; tracked {
			want[entry.Action] = true
		}
	}
	for action, seen := range want {
		if !seen {
			t.Errorf("no audit record for %s", action)
		}
	}
}
