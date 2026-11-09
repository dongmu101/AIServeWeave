package e2e_test

import (
	"net/http"
	"testing"
)

func TestAccountSalesRoutesWithoutPaymentProvider(t *testing.T) {
	h := buildHarness(t, harnessOpts{accountSales: true})
	for _, path := range []string{
		"/operator/v1/billing/accounts", "/admin/v1/billing/accounts", "/admin/v1/billing",
		"/operator/v1/billing/tenants", "/operator/v1/billing/ledger", "/admin/v1/billing/ledger",
	} {
		if got := h.call(http.MethodGet, path, "", nil, nil); got != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want authentication guard (401)", path, got)
		}
	}
	for _, path := range []string{"/operator/v1/billing/packages", "/operator/v1/billing/model-prices"} {
		if got := h.call(http.MethodGet, path, "", nil, nil); got != http.StatusNotFound {
			t.Errorf("payment route %s: got %d, want disabled (404)", path, got)
		}
	}
	if got := h.call(http.MethodPost, "/billing/v1/dodo/webhook", "", nil, nil); got != http.StatusNotFound {
		t.Errorf("Dodo webhook: got %d, want disabled (404)", got)
	}
}

func TestAccountPurchaseAndCreditLedgerWithoutPaymentProvider(t *testing.T) {
	h := buildHarness(t, harnessOpts{accountSales: true})
	operator := bootstrapPlatformOperator(h, "accounts-operator@example.com")
	tenant, owner := bootstrap(h, "Account buyer", "accounts-buyer@example.com")
	var sale struct {
		ID string `json:"id"`
	}
	draft := map[string]any{"node_id": "agent-one", "runtime_id": "codex-a", "name": "Codex", "price_micro": 5000000, "active": true}
	if got := h.call(http.MethodPost, "/operator/v1/billing/accounts", operator, draft, &sale); got != http.StatusCreated {
		t.Fatalf("create account: %d", got)
	}
	credit := map[string]any{"amount_micro": 10000000, "reason": "offline payment"}
	if got := h.call(http.MethodPost, "/operator/v1/billing/tenants/"+tenant.Tenant.ID+"/adjust", operator, credit, nil); got < 200 || got >= 300 {
		t.Fatalf("adjust credit without Dodo: %d", got)
	}
	purchase := map[string]any{"request_id": "00000000000000000000000000000001", "expected_price_micro": 5000000}
	if got := h.call(http.MethodPost, "/admin/v1/billing/accounts/"+sale.ID+"/purchase", owner, purchase, nil); got != http.StatusOK {
		t.Fatalf("purchase account without Dodo: %d", got)
	}
	var overview struct {
		Mode     string `json:"mode"`
		Balance  int64  `json:"balance_micro"`
		Blocked  bool   `json:"blocked"`
		Packages []any  `json:"packages"`
	}
	if got := h.call(http.MethodGet, "/admin/v1/billing", owner, nil, &overview); got != http.StatusOK {
		t.Fatalf("billing overview without Dodo: %d", got)
	}
	if overview.Mode != "account_monthly" || overview.Balance != 5000000 || overview.Blocked || len(overview.Packages) != 0 {
		t.Fatalf("unexpected account-only overview: %+v", overview)
	}
	for _, path := range []string{"/admin/v1/billing/accounts", "/admin/v1/billing/ledger", "/admin/v1/billing/orders"} {
		if got := h.call(http.MethodGet, path, owner, nil, nil); got != http.StatusOK {
			t.Errorf("%s: %d", path, got)
		}
	}
	if got := h.call(http.MethodGet, "/operator/v1/billing/accounts", owner, nil, nil); got != http.StatusForbidden {
		t.Errorf("tenant can access platform inventory: %d", got)
	}
}
