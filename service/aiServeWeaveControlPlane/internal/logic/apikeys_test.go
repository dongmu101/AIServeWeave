package logic_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/apikey"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// TestCreatedKeyVerifies is the happy path the whole feature exists for: a
// minted key authenticates as the tenant that minted it.
//
// TestCreatedKeyVerifies 是整个功能存在的意义所在的正常路径：铸造出的 key 能以铸造它
// 的那个租户的身份通过认证。
func TestCreatedKeyVerifies(t *testing.T) {
	f := newFixture(t)
	created := f.mustCreateKey(f.ownerAt, "production")

	got, err := f.svc.VerifyKeyHash(context.Background(), apikey.Hash(created.Plaintext))
	if err != nil {
		t.Fatalf("VerifyKeyHash on a freshly minted key: %v", err)
	}
	if got.TenantID != f.tenant.ID {
		t.Errorf("TenantID = %q, want %q", got.TenantID, f.tenant.ID)
	}
	if got.KeyID != created.Key.ID {
		t.Errorf("KeyID = %q, want %q", got.KeyID, created.Key.ID)
	}
}

// TestStoredKeyIsOnlyAHash is the executable form of the README's security
// rule: 用户 API Key 只保存不可逆哈希. It reaches past the service into the
// store, because the rule is about what is persisted, not about what an API
// returns.
//
// TestStoredKeyIsOnlyAHash 是 README 那条安全规则的可执行版本：用户 API Key 只保存
// 不可逆哈希。它越过服务直接查看 store，因为这条规则约束的是「被持久化了什么」，
// 而不是「某个 API 返回了什么」。
func TestStoredKeyIsOnlyAHash(t *testing.T) {
	f := newFixture(t)
	created := f.mustCreateKey(f.ownerAt, "production")

	stored, err := f.store.GetAPIKeyByHash(context.Background(), apikey.Hash(created.Plaintext))
	if err != nil {
		t.Fatalf("reading the stored key: %v", err)
	}

	secret := strings.TrimPrefix(created.Plaintext, apikey.Prefix)
	for name, field := range map[string]string{
		"Hash":    stored.Hash,
		"Display": stored.Display,
		"Name":    stored.Name,
	} {
		if strings.Contains(field, secret) {
			t.Errorf("stored field %s contains the key's secret verbatim", name)
		}
	}
	if stored.Hash != apikey.Hash(created.Plaintext) {
		t.Errorf("stored hash is not the hash of the plaintext")
	}
}

// TestListedKeysCarryNoSecret asserts a listing can identify keys without
// being able to reconstruct or verify one.
//
// TestListedKeysCarryNoSecret 断言列表可以标识各个 key，但既无法重建也无法据以校验
// 其中任何一个。
func TestListedKeysCarryNoSecret(t *testing.T) {
	f := newFixture(t)
	created := f.mustCreateKey(f.ownerAt, "production")

	keys, err := f.svc.ListAPIKeys(context.Background(), f.ownerAt)
	if err != nil {
		t.Fatalf("ListAPIKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("got %d keys, want 1", len(keys))
	}
	if keys[0].Hash != "" {
		t.Errorf("a listed key carries its lookup hash: %q", keys[0].Hash)
	}
	if keys[0].Display != created.Key.Display {
		t.Errorf("Display = %q, want %q", keys[0].Display, created.Key.Display)
	}
}

func TestVerifyRejectsWhatItShould(t *testing.T) {
	tests := []struct {
		name string
		// prepare returns the hash to verify, after putting the fixture into
		// the state under test.
		//
		// prepare 在把 fixture 置于被测状态之后，返回要校验的哈希。
		prepare func(f *fixture) string
	}{
		{
			name: "a key that was never issued",
			prepare: func(f *fixture) string {
				unissued, err := apikey.Generate()
				if err != nil {
					f.t.Fatalf("Generate: %v", err)
				}
				return unissued.Hash
			},
		},
		{
			name: "a revoked key",
			prepare: func(f *fixture) string {
				created := f.mustCreateKey(f.ownerAt, "leaked")
				if err := f.svc.RevokeAPIKey(context.Background(), f.ownerAt, created.Key.ID); err != nil {
					f.t.Fatalf("RevokeAPIKey: %v", err)
				}
				return apikey.Hash(created.Plaintext)
			},
		},
		{
			name: "an expired key",
			prepare: func(f *fixture) string {
				created := f.mustCreateKey(f.ownerAt, "seasonal")
				f.clock.Advance(logic.DefaultKeyLifetime + time.Second)
				return apikey.Hash(created.Plaintext)
			},
		},
		{
			name:    "a hash of the wrong length",
			prepare: func(f *fixture) string { return "short" },
		},
		{
			name:    "the empty hash",
			prepare: func(f *fixture) string { return "" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			hash := tt.prepare(f)

			_, err := f.svc.VerifyKeyHash(context.Background(), hash)
			if !errors.Is(err, logic.ErrNotFound) {
				t.Errorf("VerifyKeyHash error = %v, want ErrNotFound (every rejection looks alike)", err)
			}
		})
	}
}

// TestKeyExpiresExactlyAtItsDeadline pins the boundary, because "expires in 90
// days" is the kind of rule that silently becomes 89 or 91.
//
// TestKeyExpiresExactlyAtItsDeadline 钉住边界，因为「90 天后过期」正是那种会悄悄变成
// 89 天或 91 天的规则。
func TestKeyExpiresExactlyAtItsDeadline(t *testing.T) {
	f := newFixture(t)
	created := f.mustCreateKey(f.ownerAt, "production")
	hash := apikey.Hash(created.Plaintext)

	f.clock.Advance(logic.DefaultKeyLifetime - time.Second)
	if _, err := f.svc.VerifyKeyHash(context.Background(), hash); err != nil {
		t.Errorf("key rejected one second before its expiry: %v", err)
	}

	f.clock.Advance(2 * time.Second)
	if _, err := f.svc.VerifyKeyHash(context.Background(), hash); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("key accepted one second after its expiry: err = %v", err)
	}
}

func TestCreateAPIKeyLifetime(t *testing.T) {
	tests := []struct {
		name     string
		lifetime time.Duration
		wantErr  bool
		// wantExpiry is checked only when wantErr is false; nil means the key
		// must have no expiry at all.
		//
		// wantExpiry 仅在 wantErr 为 false 时检查；为 nil 表示该 key 必须完全没有
		// 过期时间。
		wantExpiry func(created time.Time) *time.Time
	}{
		{
			name:     "zero uses the default lifetime",
			lifetime: 0,
			wantExpiry: func(created time.Time) *time.Time {
				at := created.Add(logic.DefaultKeyLifetime)
				return &at
			},
		},
		{
			name:     "an explicit lifetime is honored",
			lifetime: 24 * time.Hour,
			wantExpiry: func(created time.Time) *time.Time {
				at := created.Add(24 * time.Hour)
				return &at
			},
		},
		{
			name:       "a negative lifetime means no expiry",
			lifetime:   -1,
			wantExpiry: func(time.Time) *time.Time { return nil },
		},
		{
			name:     "past the maximum is refused",
			lifetime: logic.MaxKeyLifetime + time.Hour,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			now := f.clock.Now()

			created, err := f.svc.CreateAPIKey(context.Background(), f.ownerAt, "k", tt.lifetime)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CreateAPIKey error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			want := tt.wantExpiry(now)
			switch {
			case want == nil && created.Key.ExpiresAt != nil:
				t.Errorf("ExpiresAt = %v, want no expiry", created.Key.ExpiresAt)
			case want != nil && created.Key.ExpiresAt == nil:
				t.Errorf("ExpiresAt is nil, want %v", *want)
			case want != nil && !created.Key.ExpiresAt.Equal(*want):
				t.Errorf("ExpiresAt = %v, want %v", *created.Key.ExpiresAt, *want)
			}
		})
	}
}

func TestCreateAPIKeyPermissions(t *testing.T) {
	tests := []struct {
		name    string
		role    string
		wantErr error
	}{
		{name: "an owner may mint", role: model.RoleOwner},
		{name: "an admin may mint", role: model.RoleAdmin},
		{name: "a member may not", role: model.RoleMember, wantErr: logic.ErrForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			_, err := f.svc.CreateAPIKey(context.Background(), f.actorWithRole(tt.role), "k", 0)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("CreateAPIKey error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestRevokeIsScopedToTheTenant is the isolation assertion: one tenant's admin
// must not be able to revoke another tenant's key, and must not be able to
// learn that it exists.
//
// TestRevokeIsScopedToTheTenant 是隔离性断言：一个租户的管理员不得吊销另一个租户的
// key，也不得据此得知它存在。
func TestRevokeIsScopedToTheTenant(t *testing.T) {
	f := newFixture(t)
	victim := f.mustCreateKey(f.ownerAt, "victim")

	// A second tenant, with its own owner.
	//
	// 第二个租户，有它自己的 owner。
	otherTenant, otherOwner, err := f.svc.CreateTenant(context.Background(), "Other", "other@example.com", testPassword, "10.0.0.9")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	intruder := logic.Actor{UserID: otherOwner.ID, TenantID: otherTenant.ID, Role: model.RoleOwner}

	if err := f.svc.RevokeAPIKey(context.Background(), intruder, victim.Key.ID); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("cross-tenant revoke error = %v, want ErrNotFound", err)
	}
	if _, err := f.svc.VerifyKeyHash(context.Background(), apikey.Hash(victim.Plaintext)); err != nil {
		t.Errorf("the victim key stopped working after a cross-tenant revoke attempt: %v", err)
	}
}

// TestMemberMayRevokeOnlyItsOwnKey covers the one permission that is not a
// plain role check.
//
// TestMemberMayRevokeOnlyItsOwnKey 覆盖唯一一个不是简单角色判断的权限。
func TestMemberMayRevokeOnlyItsOwnKey(t *testing.T) {
	f := newFixture(t)
	member := f.actorWithRole(model.RoleMember)

	// The member cannot mint, so the owner mints one on its behalf and the
	// test rewrites CreatedBy — the state a member-created key would be in.
	//
	// member 无法铸造，因此由 owner 代为铸造一个，再由测试改写 CreatedBy——那正是
	// member 自己创建的 key 会处于的状态。
	own := f.mustCreateKey(f.ownerAt, "member's own")
	stored, err := f.store.GetAPIKeyByHash(context.Background(), apikey.Hash(own.Plaintext))
	if err != nil {
		t.Fatalf("reading the stored key: %v", err)
	}
	stored.CreatedBy = member.UserID
	f.store.ReplaceAPIKey(stored)

	someoneElses := f.mustCreateKey(f.ownerAt, "the owner's")

	if err := f.svc.RevokeAPIKey(context.Background(), member, own.Key.ID); err != nil {
		t.Errorf("a member could not revoke the key it created: %v", err)
	}
	if err := f.svc.RevokeAPIKey(context.Background(), member, someoneElses.Key.ID); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("a member revoked another user's key: err = %v, want ErrNotFound", err)
	}
}

// TestRevokeTwiceKeepsTheFirstTimestamp asserts the audit trail keeps the
// moment the key actually stopped working.
//
// TestRevokeTwiceKeepsTheFirstTimestamp 断言审计线索保留的是该 key 实际停止工作的
// 那一刻。
func TestRevokeTwiceKeepsTheFirstTimestamp(t *testing.T) {
	f := newFixture(t)
	created := f.mustCreateKey(f.ownerAt, "leaked")

	if err := f.svc.RevokeAPIKey(context.Background(), f.ownerAt, created.Key.ID); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	first, err := f.store.GetAPIKeyByHash(context.Background(), apikey.Hash(created.Plaintext))
	if err != nil {
		t.Fatalf("reading the revoked key: %v", err)
	}

	f.clock.Advance(time.Hour)
	if err := f.svc.RevokeAPIKey(context.Background(), f.ownerAt, created.Key.ID); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("second revoke error = %v, want ErrNotFound", err)
	}

	second, err := f.store.GetAPIKeyByHash(context.Background(), apikey.Hash(created.Plaintext))
	if err != nil {
		t.Fatalf("reading the revoked key again: %v", err)
	}
	if !second.RevokedAt.Equal(*first.RevokedAt) {
		t.Errorf("RevokedAt moved from %v to %v on a second revoke", *first.RevokedAt, *second.RevokedAt)
	}
}

// TestKeyOperationsAreAudited asserts the README's 所有管理操作写入审计日志 holds
// for this feature, and that the record never carries the secret.
//
// TestKeyOperationsAreAudited 断言 README 的「所有管理操作写入审计日志」对本功能成立，
// 且记录中绝不携带密文。
func TestKeyOperationsAreAudited(t *testing.T) {
	f := newFixture(t)
	created := f.mustCreateKey(f.ownerAt, "production")
	if err := f.svc.RevokeAPIKey(context.Background(), f.ownerAt, created.Key.ID); err != nil {
		t.Fatalf("RevokeAPIKey: %v", err)
	}

	entries, err := f.svc.ListAudit(context.Background(), f.ownerAt, 0)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}

	actions := map[string]bool{}
	secret := strings.TrimPrefix(created.Plaintext, apikey.Prefix)
	for _, entry := range entries {
		actions[entry.Action] = true
		if strings.Contains(entry.Detail, secret) {
			t.Errorf("an audit record carries the key's secret: %q", entry.Detail)
		}
		if entry.TenantID != f.tenant.ID {
			t.Errorf("audit record tenant = %q, want %q", entry.TenantID, f.tenant.ID)
		}
	}
	for _, want := range []string{model.ActionAPIKeyCreate, model.ActionAPIKeyRevoke} {
		if !actions[want] {
			t.Errorf("no audit record for %s; got %v", want, actions)
		}
	}
}
