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
		"/operator/v1/billing/presales", "/admin/v1/billing/presales",
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
	// Assignment needs no provider; presale checkout does.
	if got := h.call(http.MethodPost, "/operator/v1/billing/orders/ord_x/assign", "", nil, nil); got != http.StatusUnauthorized {
		t.Errorf("presale assignment: got %d, want authentication guard (401)", got)
	}
	if got := h.call(http.MethodPost, "/admin/v1/billing/presales/pre_x/checkout", "", nil, nil); got != http.StatusNotFound {
		t.Errorf("presale checkout: got %d, want disabled (404)", got)
	}
}

// Without a payment provider the fiat inventory is managed and listed, but
// there is no checkout and no credit purchase.
func TestAccountInventoryWithoutPaymentProvider(t *testing.T) {
	h := buildHarness(t, harnessOpts{accountSales: true})
	operator := bootstrapPlatformOperator(h, "accounts-operator@example.com")
	_, owner := bootstrap(h, "Account buyer", "accounts-buyer@example.com")
	legacy := map[string]any{"node_id": "agent-one", "runtime_id": "codex-a", "name": "Codex", "price_micro": 5000000, "active": true}
	if got := h.call(http.MethodPost, "/operator/v1/billing/accounts", operator, legacy, nil); got != http.StatusBadRequest {
		t.Fatalf("credit-priced account accepted: %d", got)
	}
	var sale struct {
		ID string `json:"id"`
	}
	draft := map[string]any{"node_id": "agent-one", "runtime_id": "codex-a", "kind": "codex", "name": "Codex", "price_minor": 2000, "currency": "USD", "dodo_product_id": "pdt_pwyw", "active": true}
	if got := h.call(http.MethodPost, "/operator/v1/billing/accounts", operator, draft, &sale); got != http.StatusCreated {
		t.Fatalf("create account: %d", got)
	}
	var listed struct {
		Items []map[string]any `json:"items"`
	}
	if got := h.call(http.MethodGet, "/admin/v1/billing/accounts", owner, nil, &listed); got != http.StatusOK || len(listed.Items) != 1 {
		t.Fatalf("tenant inventory: %d %+v", got, listed)
	}
	if item := listed.Items[0]; item["price_minor"] != float64(2000) || item["currency"] != "USD" || item["dodo_product_id"] != nil {
		t.Fatalf("tenant inventory projection: %+v", item)
	}
	checkout := map[string]any{"expected_price_minor": 2000, "currency": "USD"}
	for _, path := range []string{"/admin/v1/billing/accounts/" + sale.ID + "/checkout", "/admin/v1/billing/accounts/" + sale.ID + "/purchase"} {
		if got := h.call(http.MethodPost, path, owner, checkout, nil); got != http.StatusNotFound {
			t.Errorf("%s without Dodo: %d, want 404", path, got)
		}
	}
	for _, path := range []string{"/admin/v1/billing", "/admin/v1/billing/ledger", "/admin/v1/billing/orders"} {
		if got := h.call(http.MethodGet, path, owner, nil, nil); got != http.StatusOK {
			t.Errorf("%s: %d", path, got)
		}
	}
	if got := h.call(http.MethodGet, "/operator/v1/billing/accounts", owner, nil, nil); got != http.StatusForbidden {
		t.Errorf("tenant can access platform inventory: %d", got)
	}
}
