package memstore

import (
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"context"
	"sort"
	"time"
)

// presaleOutstanding counts presaleID's orders PresaleOrderOutstanding
// counts. The caller holds s.mu.
//
// presaleOutstanding 统计 presaleID 下按 PresaleOrderOutstanding 计入的订单数。调用方
// 须持有 s.mu。
func (s *Store) presaleOutstanding(presaleID string, now time.Time) int {
	n := 0
	for _, order := range s.billing.orders {
		if order.PresaleID == presaleID && store.PresaleOrderOutstanding(order, now) {
			n++
		}
	}
	return n
}

// ListAccountPresales implements store.AccountPresales.
//
// ListAccountPresales 实现 store.AccountPresales。
func (s *Store) ListAccountPresales(_ context.Context, now time.Time) ([]store.PresaleStock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.PresaleStock, 0, len(s.billing.presales))
	for _, row := range s.billing.presales {
		out = append(out, store.PresaleStock{AccountPresale: row, Outstanding: s.presaleOutstanding(row.ID, now)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// SaveAccountPresale implements store.AccountPresales.
//
// SaveAccountPresale 实现 store.AccountPresales。
func (s *Store) SaveAccountPresale(_ context.Context, row model.AccountPresale, create bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.billing.presales[row.ID]
	if create {
		if exists || len(s.billing.presales) >= model.MaxPresales {
			return store.ErrConflict
		}
	} else {
		if !exists {
			return store.ErrNotFound
		}
		if old.Kind != row.Kind {
			return store.ErrConflict
		}
	}
	s.billing.presales[row.ID] = row
	return nil
}

// CreatePresaleCheckoutOrder implements store.AccountPresales.
//
// CreatePresaleCheckoutOrder 实现 store.AccountPresales。
func (s *Store) CreatePresaleCheckoutOrder(_ context.Context, order *model.CheckoutOrder, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	presale, ok := s.billing.presales[order.PresaleID]
	if !ok {
		return store.ErrNotFound
	}
	if err := store.PresaleCheckoutTerms(presale, order.Provider, order.AmountMinor, order.Currency, s.presaleOutstanding(presale.ID, now)); err != nil {
		return err
	}
	if _, ok := s.billing.orders[order.ID]; ok {
		return store.ErrConflict
	}
	s.billing.orders[order.ID] = *order
	return nil
}

// AssignPresaleOrder implements store.AccountPresales.
//
// AssignPresaleOrder 实现 store.AccountPresales。
func (s *Store) AssignPresaleOrder(_ context.Context, orderID, accountID string, now time.Time) (model.CheckoutOrder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	restore := s.snapshotBilling()
	order, err := store.AssignPresaleRules(memLedger{s}, orderID, accountID, now)
	if err != nil {
		restore()
		return model.CheckoutOrder{}, err
	}
	return order, nil
}

// GetCheckoutOrder implements store.AccountPresales.
//
// GetCheckoutOrder 实现 store.AccountPresales。
func (s *Store) GetCheckoutOrder(_ context.Context, id string) (model.CheckoutOrder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	order, ok := s.billing.orders[id]
	if !ok {
		return model.CheckoutOrder{}, store.ErrNotFound
	}
	return order, nil
}

func (l memLedger) PresaleByID(id string) (model.AccountPresale, error) {
	presale, ok := l.s.billing.presales[id]
	if !ok {
		return model.AccountPresale{}, store.ErrNotFound
	}
	return presale, nil
}

func (l memLedger) AccountCheckoutHeldByOther(accountID, tenantID string, now time.Time) (bool, error) {
	for _, other := range l.s.billing.orders {
		if other.AccountID == accountID && store.AccountCheckoutHeld(other, tenantID, now) {
			return true, nil
		}
	}
	return false, nil
}
