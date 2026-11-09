package handler

import (
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"context"
	"encoding/json"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type accountHandlerProvider struct{ billing.Provider }

func TestAccountPurchaseHTTPAndSafeProjection(t *testing.T) {
	st := memstore.New()
	ctx := context.Background()
	clock := runtime.NewSystemClock()
	service := logic.New(st, clock, logic.WithBilling(logic.BillingConfig{Provider: &accountHandlerProvider{}}))
	services := &svc.ServiceContext{Logic: service}
	if err := st.SaveAccountSale(ctx, model.AccountSale{ID: "acc_a", NodeID: "agent", RuntimeID: "claude-a", Name: "Claude A", PriceMicro: 100, Active: true}, true); err != nil {
		t.Fatal(err)
	}
	actor := logic.Actor{TenantID: "buyer", UserID: "owner", Role: model.RoleOwner}
	call := func(body string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/admin/v1/billing/accounts/acc_a/purchase", strings.NewReader(body))
		r = pathvar.WithVars(r, map[string]string{"id": "acc_a"})
		if authenticated {
			r = r.WithContext(withActor(r.Context(), actor))
		}
		w := httptest.NewRecorder()
		buyAccount(services)(w, r)
		return w
	}
	body := `{"request_id":"00000000000000000000000000000001","expected_price_micro":100}`
	if w := call(body, false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call(`{"tenant_id":"another"}`, true); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := call(body, true); w.Code != 402 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := st.InitTenantBilling(ctx, model.TenantBilling{TenantID: actor.TenantID, Mode: model.BillingModePrepaid, BalanceMicro: 100}, nil); err != nil {
		t.Fatal(err)
	}
	if w := call(body, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(body, true); w.Code != 200 {
		t.Fatal("idempotent retry", w.Code)
	}
	r := httptest.NewRequest(http.MethodGet, "/admin/v1/billing/accounts?tenant_id=other", nil)
	r = r.WithContext(withActor(r.Context(), actor))
	w := httptest.NewRecorder()
	listAccountSales(services)(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "tenant_id") {
		t.Fatal(w.Code, w.Body.String())
	}
	var list struct {
		Items []struct {
			Owned     bool      `json:"owned"`
			ExpiresAt time.Time `json:"expires_at"`
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || !list.Items[0].Owned || list.Items[0].ExpiresAt.IsZero() {
		t.Fatal(w.Body.String(), err)
	}
	policy, err := service.AccountPolicy(ctx, actor.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	wire := renderVerification(logic.Verification{TenantID: actor.TenantID, Accounts: &policy})
	if !wire.Accounts.Only || len(wire.Accounts.Managed) != 1 {
		t.Fatal("verification dropped account policy", wire)
	}
}
