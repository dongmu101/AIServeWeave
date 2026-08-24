package logic_test

import (
	"context"
	"errors"
	"testing"

	"AIServeWeave/common/apikey"
	"AIServeWeave/common/quota"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

func TestSetTenantLimitsPermissions(t *testing.T) {
	tests := []struct {
		name    string
		role    string
		wantErr error
	}{
		{name: "owner may set limits", role: model.RoleOwner},
		{name: "admin may set limits", role: model.RoleAdmin},
		// A member raising its own tenant's limit would be the cheapest way
		// past it, so the check is not about tidiness.
		//
		// member 若能调高自己租户的限制，那就是绕过它最省事的办法，所以这条检查
		// 不是为了整洁。
		{name: "member may not", role: model.RoleMember, wantErr: logic.ErrForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			limits := quota.Limits{RequestsPerMinute: 60, TokensPerMinute: 100000, MaxConcurrent: 4}
			got, err := f.svc.SetTenantLimits(context.Background(), f.actorWithRole(tt.role), limits)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("SetTenantLimits() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SetTenantLimits() error = %v, want nil", err)
			}
			if got != limits {
				t.Errorf("SetTenantLimits() = %+v, want %+v", got, limits)
			}
		})
	}
}

func TestSetTenantLimitsRejectsNegative(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.SetTenantLimits(context.Background(), f.ownerAt, quota.Limits{RequestsPerMinute: -1})
	if !errors.Is(err, logic.ErrInvalidInput) {
		t.Errorf("SetTenantLimits() error = %v, want ErrInvalidInput", err)
	}
}

// TestVerifyKeyHashCarriesTheTenantsLimits is what makes enforcement free on
// the request path: the Gateway learns the limits with the identity, so it
// never has to ask a second time.
//
// TestVerifyKeyHashCarriesTheTenantsLimits 正是让执行在请求路径上零成本的原因：
// Gateway 与身份一并得知限制，因此无需再问第二次。
func TestVerifyKeyHashCarriesTheTenantsLimits(t *testing.T) {
	f := newFixture(t)
	limits := quota.Limits{RequestsPerMinute: 60, TokensPerMinute: 100000, MaxConcurrent: 4}
	if _, err := f.svc.SetTenantLimits(context.Background(), f.ownerAt, limits); err != nil {
		t.Fatalf("SetTenantLimits: %v", err)
	}
	created := f.mustCreateKey(f.ownerAt, "test")

	got, err := f.svc.VerifyKeyHash(context.Background(), apikey.Hash(created.Plaintext))
	if err != nil {
		t.Fatalf("VerifyKeyHash() error = %v, want nil", err)
	}
	if got.Limits != limits {
		t.Errorf("Limits = %+v, want %+v", got.Limits, limits)
	}
	if got.TenantID != f.tenant.ID {
		t.Errorf("TenantID = %q, want %q", got.TenantID, f.tenant.ID)
	}
}

func TestVerifyKeyHashDefaultsToUnlimited(t *testing.T) {
	f := newFixture(t)
	created := f.mustCreateKey(f.ownerAt, "test")

	got, err := f.svc.VerifyKeyHash(context.Background(), apikey.Hash(created.Plaintext))
	if err != nil {
		t.Fatalf("VerifyKeyHash() error = %v, want nil", err)
	}
	// A tenant nobody configured limits for must not start being rejected the
	// day this feature ships.
	//
	// 一个没人为它配置过限制的租户，不能在本功能上线那天开始被拒绝。
	if !got.Limits.Unlimited() {
		t.Errorf("Limits = %+v, want unlimited for an unconfigured tenant", got.Limits)
	}
}

// TestVerifyKeyHashRejectsASuspendedTenantsKeys closes a gap that predates
// this change: suspension is an administrative hold, and a hold that leaves
// the tenant's keys working on the inference path — the one path those keys
// are actually used on — holds nothing.
//
// TestVerifyKeyHashRejectsASuspendedTenantsKeys 关闭一个早于本次改动就存在的缺口：
// 暂停是一次管理性的冻结，而一次让租户的 key 在推理路径上照常工作的冻结——那正是这些
// key 真正被使用的唯一路径——什么也没冻住。
func TestVerifyKeyHashRejectsASuspendedTenantsKeys(t *testing.T) {
	f := newFixture(t)
	created := f.mustCreateKey(f.ownerAt, "test")
	hash := apikey.Hash(created.Plaintext)

	if _, err := f.svc.VerifyKeyHash(context.Background(), hash); err != nil {
		t.Fatalf("VerifyKeyHash() before suspension: %v", err)
	}

	tenant, err := f.store.GetTenant(context.Background(), f.tenant.ID)
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	tenant.Status = model.StatusSuspended
	f.store.PutTenant(tenant)

	if _, err := f.svc.VerifyKeyHash(context.Background(), hash); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("VerifyKeyHash() for a suspended tenant = %v, want ErrNotFound", err)
	}
}
