package memstore

import (
	"AIServeWeave/common/accountaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"context"
	"sort"
	"time"
)

func (s *Store) ListAccountSales(_ context.Context) ([]model.AccountSale, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]model.AccountSale, 0, len(s.billing.accounts))
	for _, row := range s.billing.accounts {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}

func (s *Store) SaveAccountSale(_ context.Context, row model.AccountSale, create bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.billing.accounts[row.ID]
	if create {
		if exists || len(s.billing.accounts) >= accountaccess.MaxAccounts {
			return store.ErrConflict
		}
		for _, other := range s.billing.accounts {
			if other.NodeID == row.NodeID && other.RuntimeID == row.RuntimeID {
				return store.ErrConflict
			}
		}
	} else {
		if !exists || old.NodeID != row.NodeID || old.RuntimeID != row.RuntimeID || (old.Kind != "" && old.Kind != row.Kind) {
			return store.ErrConflict
		}
		row.TenantID, row.ExpiresAt = old.TenantID, old.ExpiresAt
	}
	s.billing.accounts[row.ID] = row
	return nil
}

// CreateAccountCheckoutOrder implements store.AccountSales.
//
// CreateAccountCheckoutOrder 实现 store.AccountSales。
func (s *Store) CreateAccountCheckoutOrder(_ context.Context, order *model.CheckoutOrder, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sale, ok := s.billing.accounts[order.AccountID]
	if !ok {
		return store.ErrNotFound
	}
	if err := store.AccountCheckoutTerms(sale, order.TenantID, order.AmountMinor, order.Currency, now); err != nil {
		return err
	}
	for _, other := range s.billing.orders {
		if other.AccountID == order.AccountID && store.AccountCheckoutHeld(other, order.TenantID, now) {
			return store.ErrConflict
		}
	}
	if _, ok := s.billing.orders[order.ID]; ok {
		return store.ErrConflict
	}
	s.billing.orders[order.ID] = *order
	return nil
}

// FailCheckoutOrder implements store.AccountSales.
//
// FailCheckoutOrder 实现 store.AccountSales。
func (s *Store) FailCheckoutOrder(_ context.Context, orderID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if order, ok := s.billing.orders[orderID]; ok && order.Status == model.OrderStatusPending {
		order.Status, order.UpdatedAt = model.OrderStatusFailed, now
		s.billing.orders[orderID] = order
	}
	return nil
}

func (l memLedger) AccountSaleByID(id string) (model.AccountSale, error) {
	sale, ok := l.s.billing.accounts[id]
	if !ok {
		return model.AccountSale{}, store.ErrNotFound
	}
	return sale, nil
}

func (l memLedger) GrantAccount(accountID, tenantID string, expiresAt, now time.Time) error {
	sale, ok := l.s.billing.accounts[accountID]
	if !ok {
		return store.ErrNotFound
	}
	row := l.s.billingRow(tenantID, now)
	row.Mode, row.UpdatedAt = model.BillingModeAccountMonthly, now
	l.s.billing.tenants[tenantID] = row
	sale.TenantID, sale.ExpiresAt, sale.UpdatedAt = tenantID, expiresAt, now
	l.s.billing.accounts[accountID] = sale
	return nil
}
