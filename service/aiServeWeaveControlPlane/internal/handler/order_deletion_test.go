package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"github.com/zeromicro/go-zero/rest/pathvar"
)

func TestDeleteBillingOrderHTTP(t *testing.T) {
	st := memstore.New()
	ctx := context.Background()
	service := logic.New(st, runtime.NewSystemClock(), logic.WithAccountSales())
	services := &svc.ServiceContext{Logic: service}
	order := model.CheckoutOrder{ID: "ord_delete", TenantID: "buyer", Status: model.OrderStatusPending, CreatedAt: time.Now()}
	if err := st.CreateCheckoutOrder(ctx, &order); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, tenant, role string
		want               int
	}{
		{"unauthenticated", "", "", http.StatusUnauthorized},
		{"member", "buyer", model.RoleMember, http.StatusForbidden},
		{"foreign", "other", model.RoleOwner, http.StatusNotFound},
		{"admin", "buyer", model.RoleAdmin, http.StatusNoContent},
		{"already deleted", "buyer", model.RoleOwner, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodDelete, "/admin/v1/billing/orders/ord_delete?tenant_id=other", nil)
			r = pathvar.WithVars(r, map[string]string{"id": order.ID})
			if tc.role != "" {
				r = r.WithContext(withActor(r.Context(), logic.Actor{TenantID: tc.tenant, UserID: "user", Role: tc.role}))
			}
			w := httptest.NewRecorder()
			deleteBillingOrder(services)(w, r)
			if w.Code != tc.want || (tc.want == http.StatusNoContent && w.Body.Len() != 0) {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
