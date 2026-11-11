package memstore

import (
	"context"
	"maps"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreateAlipayPurchase implements store.AlipayBilling.
//
// CreateAlipayPurchase 实现 store.AlipayBilling。
func (s *Store) CreateAlipayPurchase(_ context.Context, row model.AlipayPurchase) (model.AlipayPurchase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.billing.alipayPurchases {
		if existing.TenantID == row.TenantID && existing.ActionID == row.ActionID {
			if existing.SkuID != row.SkuID {
				return model.AlipayPurchase{}, store.ErrConflict
			}
			return existing, nil
		}
	}
	if _, taken := s.billing.alipayPurchases[row.ID]; taken {
		return model.AlipayPurchase{}, store.ErrConflict
	}
	s.billing.alipayPurchases[row.ID] = row
	return row, nil
}

// SaveAlipayPurchase implements store.AlipayBilling.
//
// SaveAlipayPurchase 实现 store.AlipayBilling。
func (s *Store) SaveAlipayPurchase(_ context.Context, row model.AlipayPurchase) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.billing.alipayPurchases[row.ID]
	if !ok {
		return store.ErrNotFound
	}
	existing.Status, existing.PurchaseURL, existing.ExpireTime = row.Status, row.PurchaseURL, row.ExpireTime
	existing.ReasonCode, existing.UpdatedAt = row.ReasonCode, row.UpdatedAt
	s.billing.alipayPurchases[row.ID] = existing
	return nil
}

// CreateAlipayRedemption implements store.AlipayBilling.
//
// CreateAlipayRedemption 实现 store.AlipayBilling。
func (s *Store) CreateAlipayRedemption(_ context.Context, row model.AlipayRedemption) (model.AlipayRedemption, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	restore := s.snapshotAlipay()
	out, err := store.CreateAlipayRedemptionRules(memLedger{s}, row, row.CreatedAt)
	if err != nil {
		restore()
		return model.AlipayRedemption{}, err
	}
	return out, nil
}

// SettleAlipayRedemption implements store.AlipayBilling.
//
// SettleAlipayRedemption 实现 store.AlipayBilling。
func (s *Store) SettleAlipayRedemption(_ context.Context, outcome store.AlipayOutcome, now time.Time) (model.AlipayRedemption, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	restore := s.snapshotAlipay()
	out, changed, err := store.SettleAlipayRedemptionRules(memLedger{s}, outcome, now)
	if err != nil {
		restore()
		return model.AlipayRedemption{}, false, err
	}
	return out, changed, nil
}

// snapshotAlipay extends snapshotBilling with the redemptions. The caller
// holds s.mu.
func (s *Store) snapshotAlipay() func() {
	billing := s.snapshotBilling()
	redemptions := maps.Clone(s.billing.alipayRedemptions)
	return func() {
		billing()
		s.billing.alipayRedemptions = redemptions
	}
}

// GetAlipayRedemption implements store.AlipayBilling.
//
// GetAlipayRedemption 实现 store.AlipayBilling。
func (s *Store) GetAlipayRedemption(_ context.Context, id string) (model.AlipayRedemption, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.billing.alipayRedemptions[id]
	if !ok {
		return model.AlipayRedemption{}, store.ErrNotFound
	}
	return row, nil
}

// ListAlipayRedemptions implements store.AlipayBilling.
//
// ListAlipayRedemptions 实现 store.AlipayBilling。
func (s *Store) ListAlipayRedemptions(_ context.Context, query store.ListQuery, filter store.AlipayRedemptionFilter) (store.Page[model.AlipayRedemption], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []model.AlipayRedemption
	for _, r := range s.billing.alipayRedemptions {
		if (filter.TenantID == "" || r.TenantID == filter.TenantID) &&
			(filter.Status == "" || r.Status == filter.Status) &&
			(filter.CreatedBefore.IsZero() || r.CreatedAt.Before(filter.CreatedBefore)) {
			items = append(items, r)
		}
	}
	key := func(r model.AlipayRedemption) (time.Time, string) { return r.CreatedAt, r.ID }
	sortNewestFirst(items, key)
	return paginate(items, query, key)
}

var _ store.AlipayTx = memLedger{}

func (l memLedger) AlipayRedemptionByID(id string) (model.AlipayRedemption, error) {
	row, ok := l.s.billing.alipayRedemptions[id]
	if !ok {
		return model.AlipayRedemption{}, store.ErrNotFound
	}
	return row, nil
}

func (l memLedger) AlipayRedemptionByAction(tenantID, actionID string) (model.AlipayRedemption, error) {
	for _, r := range l.s.billing.alipayRedemptions {
		if r.TenantID == tenantID && r.ActionID == actionID {
			return r, nil
		}
	}
	return model.AlipayRedemption{}, store.ErrNotFound
}

func (l memLedger) AlipayPendingRedemption(tenantID string) (bool, error) {
	for _, r := range l.s.billing.alipayRedemptions {
		if r.TenantID == tenantID && r.Status == model.AlipayRedemptionPending {
			return true, nil
		}
	}
	return false, nil
}

func (l memLedger) InsertAlipayRedemption(row model.AlipayRedemption) error {
	if _, taken := l.s.billing.alipayRedemptions[row.ID]; taken {
		return store.ErrConflict
	}
	l.s.billing.alipayRedemptions[row.ID] = row
	return nil
}

func (l memLedger) SaveAlipayRedemption(row model.AlipayRedemption) error {
	if _, ok := l.s.billing.alipayRedemptions[row.ID]; !ok {
		return store.ErrNotFound
	}
	l.s.billing.alipayRedemptions[row.ID] = row
	return nil
}
