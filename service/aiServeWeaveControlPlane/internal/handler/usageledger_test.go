package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// TestLedgerShowsPendingUsageAndPeriods checks that the ledger reports the
// usage not yet sealed and, once sealed, the entry's period and request
// count; and that an operator sees a tenant's pending usage only when naming
// the tenant.
func TestLedgerShowsPendingUsageAndPeriods(t *testing.T) {
	st := memstore.New()
	ctx := context.Background()
	t0 := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	services := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock(), logic.WithBilling(logic.BillingConfig{Provider: &accountHandlerProvider{}}))}
	if err := st.InitTenantBilling(ctx, model.TenantBilling{TenantID: "tnt_l", Mode: model.BillingModePrepaid, BalanceMicro: 1000, CreatedAt: t0, UpdatedAt: t0}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ChargeUsage(ctx, []model.UsageRecord{
		{ID: "req_l1", TenantID: "tnt_l", Model: "m", Endpoint: "chat", PromptTokens: 1, TotalTokens: 1, CreatedAt: t0, CostMicro: 30},
		{ID: "req_l2", TenantID: "tnt_l", Model: "m", Endpoint: "chat", PromptTokens: 1, TotalTokens: 1, CreatedAt: t0, CostMicro: 20},
	}, t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	read := func(target string, actor logic.Actor, operator bool) types.LedgerListResponse {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, target, nil).WithContext(withActor(ctx, actor))
		w := httptest.NewRecorder()
		listBillingLedger(services, operator)(w, r)
		var out types.LedgerListResponse
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		return out
	}
	tenantActor := logic.Actor{TenantID: "tnt_l", UserID: "usr", Role: model.RoleOwner}
	operator := logic.Actor{TenantID: model.PlatformScope, UserID: "operator", Role: model.RolePlatformOperator}

	got := read("/admin/v1/billing/ledger", tenantActor, false)
	if len(got.Items) != 0 || got.PendingUsage == nil || got.PendingUsage.AmountMicro != -50 || got.PendingUsage.RequestCount != 2 ||
		got.PendingUsage.SealAt == nil || !got.PendingUsage.SealAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("tenant ledger = %+v", got)
	}
	if read("/operator/v1/billing/ledger", operator, true).PendingUsage != nil {
		t.Fatal("a cross-tenant ledger has no single pending accumulation")
	}
	if p := read("/operator/v1/billing/ledger?tenant_id=tnt_l", operator, true).PendingUsage; p == nil || p.AmountMicro != -50 {
		t.Fatalf("operator ledger for one tenant: pending = %+v", p)
	}

	if _, err := st.SealDueUsage(ctx, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got = read("/admin/v1/billing/ledger", tenantActor, false)
	if got.PendingUsage != nil || len(got.Items) != 1 {
		t.Fatalf("after sealing = %+v", got)
	}
	if e := got.Items[0]; e.Kind != model.LedgerKindUsage || e.AmountMicro != -50 || e.BalanceAfterMicro != 950 || e.RequestCount != 2 ||
		e.PeriodStart == nil || !e.PeriodStart.Equal(t0) || e.PeriodEnd == nil {
		t.Fatalf("sealed entry = %+v", e)
	}
}
