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
		if !exists || old.NodeID != row.NodeID || old.RuntimeID != row.RuntimeID {
			return store.ErrConflict
		}
		row.TenantID, row.ExpiresAt = old.TenantID, old.ExpiresAt
	}
	s.billing.accounts[row.ID] = row
	return nil
}

func (s *Store) BuyAccount(_ context.Context, accountID, tenantID, actorID, requestID string, price int64, now time.Time) (model.AccountPurchase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if receipt, ok := s.billing.purchases[requestID]; ok {
		if receipt.TenantID != tenantID || receipt.AccountID != accountID || receipt.PriceMicro != price {
			return model.AccountPurchase{}, store.ErrConflict
		}
		return receipt, nil
	}
	sale, ok := s.billing.accounts[accountID]
	if !ok {
		return model.AccountPurchase{}, store.ErrNotFound
	}
	end, err := store.AccountPurchaseTerms(sale, tenantID, price, now)
	if err != nil {
		return model.AccountPurchase{}, err
	}
	balance := s.billing.tenants[tenantID]
	if balance.BalanceMicro < price {
		return model.AccountPurchase{}, store.ErrInsufficientCredit
	}
	balance.BalanceMicro -= price
	balance.Mode, balance.UpdatedAt = model.BillingModeAccountMonthly, now
	s.billing.tenants[tenantID] = balance
	sale.TenantID, sale.ExpiresAt, sale.UpdatedAt = tenantID, end, now
	s.billing.accounts[accountID] = sale
	receipt := model.AccountPurchase{ID: requestID, AccountID: accountID, TenantID: tenantID, PriceMicro: price, ExpiresAt: end, CreatedAt: now}
	s.billing.purchases[requestID] = receipt
	key := "account:" + requestID
	s.billing.ledger = append(s.billing.ledger, model.CreditLedgerEntry{ID: model.NewID(model.PrefixCreditLedger), TenantID: tenantID, Kind: model.LedgerKindAccountPurchase,
		AmountMicro: -price, BalanceAfterMicro: balance.BalanceMicro, Ref: accountID, ActorID: actorID, IdempotencyKey: &key, CreatedAt: now})
	return receipt, nil
}
