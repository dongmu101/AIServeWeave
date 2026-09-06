package logic_test

import (
	"context"
	"errors"
	"testing"

	"AIServeWeave/common/quota"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// TestCurrentTenantIsReadableByEveryRole covers the asymmetry between reading
// a quota and writing one: writing is owner and admin only, reading is not.
// A member who cannot raise a limit still needs to see the limit that is
// throttling them.
//
// TestCurrentTenantIsReadableByEveryRole 覆盖「读配额」与「写配额」之间的不对称：写仅限
// owner 与 admin，读则不限。一个无法调高限制的 member，依然需要看到正在限流他的那条限制。
func TestCurrentTenantIsReadableByEveryRole(t *testing.T) {
	f := newFixture(t)
	limits := quota.Limits{RequestsPerMinute: 600, TokensPerMinute: 90000, MaxConcurrent: 8}
	if _, err := f.svc.SetTenantLimits(context.Background(), f.ownerAt, limits); err != nil {
		t.Fatalf("SetTenantLimits: %v", err)
	}

	for _, role := range []string{model.RoleOwner, model.RoleAdmin, model.RoleMember} {
		t.Run(role, func(t *testing.T) {
			tenant, err := f.svc.CurrentTenant(context.Background(), f.actorWithRole(role))
			if err != nil {
				t.Fatalf("CurrentTenant() error = %v, want nil", err)
			}
			if tenant.ID != f.tenant.ID {
				t.Errorf("ID = %q, want %q", tenant.ID, f.tenant.ID)
			}
			if tenant.Name != f.tenant.Name {
				t.Errorf("Name = %q, want %q", tenant.Name, f.tenant.Name)
			}
			if got := tenant.Limits(); got != limits {
				t.Errorf("Limits() = %+v, want %+v", got, limits)
			}
		})
	}
}

// TestCurrentTenantReadsBackWhatWasWritten asserts a quota written through the
// API is the quota the next read returns, including the zeroes that mean
// unlimited — the case a Console must not mistake for "unknown".
//
// TestCurrentTenantReadsBackWhatWasWritten 断言通过 API 写入的配额，就是下一次读取所
// 返回的配额，零值也包含在内——零表示不限制，而 Console 绝不能把它误当作「未知」。
func TestCurrentTenantReadsBackWhatWasWritten(t *testing.T) {
	tests := []struct {
		name   string
		limits quota.Limits
	}{
		{name: "every dimension bounded", limits: quota.Limits{RequestsPerMinute: 60, TokensPerMinute: 1000, MaxConcurrent: 2}},
		{name: "one dimension bounded", limits: quota.Limits{TokensPerMinute: 1000}},
		{name: "unlimited in every dimension", limits: quota.Limits{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			written, err := f.svc.SetTenantLimits(context.Background(), f.ownerAt, tt.limits)
			if err != nil {
				t.Fatalf("SetTenantLimits: %v", err)
			}
			tenant, err := f.svc.CurrentTenant(context.Background(), f.ownerAt)
			if err != nil {
				t.Fatalf("CurrentTenant: %v", err)
			}
			if got := tenant.Limits(); got != written {
				t.Errorf("CurrentTenant().Limits() = %+v, want the written %+v", got, written)
			}
		})
	}
}

// TestCurrentTenantIsScopedToTheSession asserts the tenant comes from the
// actor and cannot be aimed elsewhere: an actor holding an id that is not a
// tenant reads nothing rather than reading somebody's.
//
// TestCurrentTenantIsScopedToTheSession 断言租户来自 actor 且无法被指向别处：持有一个
// 并非租户 id 的 actor 什么也读不到，而不是读到某个人的。
func TestCurrentTenantIsScopedToTheSession(t *testing.T) {
	f := newFixture(t)
	stranger := logic.Actor{
		UserID:   model.NewID(model.PrefixUser),
		TenantID: model.NewID(model.PrefixTenant),
		Role:     model.RoleOwner,
	}

	if _, err := f.svc.CurrentTenant(context.Background(), stranger); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("CurrentTenant() error = %v, want ErrNotFound", err)
	}
}
