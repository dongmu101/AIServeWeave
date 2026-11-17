package memstore

import (
	"context"
	"strconv"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// pricingConfigState is the in-memory pricing configuration. Its zero value
// is ready to use.
//
// pricingConfigState 是内存中的定价配置。它的零值即可直接使用。
type pricingConfigState struct {
	revisions   []model.PricingRevision
	tenantPlans []model.TenantPricePlan
	version     int64
}

// LatestPricingRevision implements store.PricingConfig.
//
// LatestPricingRevision 实现 store.PricingConfig。
func (s *Store) LatestPricingRevision(_ context.Context) (model.PricingRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latestPricingRevisionLocked()
}

func (s *Store) latestPricingRevisionLocked() (model.PricingRevision, error) {
	revs := s.pricingConfig.revisions
	if len(revs) == 0 {
		return model.PricingRevision{}, store.ErrNotFound
	}
	return revs[len(revs)-1], nil
}

// PricingRevisionAt implements store.PricingConfig. Revisions are appended
// in id order with non-decreasing EffectiveAt, so the last one not after at
// is the newest in effect.
//
// PricingRevisionAt 实现 store.PricingConfig。版本按 id 顺序追加，EffectiveAt 不递减，
// 因此最后一个不晚于 at 的版本就是生效中的最新版本。
func (s *Store) PricingRevisionAt(_ context.Context, at time.Time) (model.PricingRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	revs := s.pricingConfig.revisions
	if len(revs) == 0 {
		return model.PricingRevision{}, store.ErrNotFound
	}
	found := revs[0]
	for _, rev := range revs {
		if rev.EffectiveAt.After(at) {
			break
		}
		found = rev
	}
	return found, nil
}

// GetPricingRevision implements store.PricingConfig.
//
// GetPricingRevision 实现 store.PricingConfig。
func (s *Store) GetPricingRevision(_ context.Context, id int64) (model.PricingRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rev := range s.pricingConfig.revisions {
		if rev.ID == id {
			return rev, nil
		}
	}
	return model.PricingRevision{}, store.ErrNotFound
}

// ListPricingRevisions implements store.PricingConfig.
//
// ListPricingRevisions 实现 store.PricingConfig。
func (s *Store) ListPricingRevisions(_ context.Context, query store.ListQuery) (store.Page[model.PricingRevision], error) {
	after := int64(0)
	if query.Cursor != "" {
		at, raw, err := store.DecodeCursor(query.Cursor)
		if err != nil {
			return store.Page[model.PricingRevision]{}, err
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 || !at.IsZero() {
			return store.Page[model.PricingRevision]{}, store.ErrInvalidCursor
		}
		after = id
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	size := query.Size()
	var out []model.PricingRevision
	revs := s.pricingConfig.revisions
	for i := len(revs) - 1; i >= 0; i-- {
		rev := revs[i]
		if after > 0 && rev.ID >= after {
			continue
		}
		rev.Document = ""
		out = append(out, rev)
		if len(out) > size {
			break
		}
	}
	if len(out) <= size {
		return store.Page[model.PricingRevision]{Items: out}, nil
	}
	last := out[size-1].ID
	return store.Page[model.PricingRevision]{
		Items: out[:size], NextCursor: store.EncodeCursor(time.Time{}, strconv.FormatInt(last, 10)),
	}, nil
}

// CreateInitialPricingRevision implements store.PricingConfig.
//
// CreateInitialPricingRevision 实现 store.PricingConfig。
func (s *Store) CreateInitialPricingRevision(_ context.Context, rev model.PricingRevision) (model.PricingRevision, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if latest, err := s.latestPricingRevisionLocked(); err == nil {
		return latest, false, nil
	}
	rev.ID = 1
	s.pricingConfig.revisions = append(s.pricingConfig.revisions, rev)
	s.pricingConfig.version++
	return rev, true, nil
}

// AppendPricingRevision implements store.PricingConfig.
//
// AppendPricingRevision 实现 store.PricingConfig。
func (s *Store) AppendPricingRevision(_ context.Context, in store.AppendPricingRevision) (model.PricingRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	latest, err := s.latestPricingRevisionLocked()
	if err != nil || latest.ID != in.BaseRevisionID {
		return model.PricingRevision{}, store.ErrPricingRevisionChanged
	}
	for _, plan := range in.UnusedPlans {
		for _, row := range s.pricingConfig.tenantPlans {
			if row.PlanID == plan && row.ValidTo == nil {
				return model.PricingRevision{}, store.ErrPricePlanInUse
			}
		}
	}
	effective := in.Now
	if effective.Before(latest.EffectiveAt) {
		effective = latest.EffectiveAt
	}
	rev := model.PricingRevision{
		ID: latest.ID + 1, EffectiveAt: effective, Document: in.Document, CreatedBy: in.CreatedBy,
		Summary: in.Summary, CreatedAt: in.Now,
	}
	s.pricingConfig.revisions = append(s.pricingConfig.revisions, rev)
	s.pricingConfig.version++
	return rev, nil
}

// SetTenantPricePlan implements store.PricingConfig.
//
// SetTenantPricePlan 实现 store.PricingConfig。
func (s *Store) SetTenantPricePlan(_ context.Context, in store.SetTenantPricePlan) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	latest, err := s.latestPricingRevisionLocked()
	if err != nil || latest.ID != in.ExpectRevisionID {
		return false, store.ErrPricingRevisionChanged
	}
	rows := s.pricingConfig.tenantPlans
	current, open := "", 0
	at := in.Now
	for _, row := range rows {
		if row.TenantID != in.TenantID || row.ValidTo != nil {
			continue
		}
		current = row.PlanID
		open++
		if at.Before(row.ValidFrom) {
			at = row.ValidFrom
		}
	}
	if current == in.PlanID && open <= 1 {
		return false, nil
	}
	for i := range rows {
		if rows[i].TenantID == in.TenantID && rows[i].ValidTo == nil {
			closed := at
			rows[i].ValidTo = &closed
		}
	}
	if in.PlanID != "" {
		s.pricingConfig.tenantPlans = append(s.pricingConfig.tenantPlans, model.TenantPricePlan{
			ID: model.NewID(model.PrefixTenantPricePlan), TenantID: in.TenantID, PlanID: in.PlanID,
			ValidFrom: at, CreatedBy: in.CreatedBy,
		})
	}
	s.pricingConfig.version++
	return true, nil
}

// TenantPricePlansAt implements store.PricingConfig.
//
// TenantPricePlansAt 实现 store.PricingConfig。
func (s *Store) TenantPricePlansAt(_ context.Context, tenantIDs []string, at time.Time) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := make(map[string]bool, len(tenantIDs))
	for _, id := range tenantIDs {
		want[id] = true
	}
	out := make(map[string]string, len(tenantIDs))
	for _, row := range s.pricingConfig.tenantPlans {
		if !want[row.TenantID] || row.ValidFrom.After(at) || (row.ValidTo != nil && !row.ValidTo.After(at)) {
			continue
		}
		out[row.TenantID] = row.PlanID
	}
	return out, nil
}

// CountTenantPricePlans implements store.PricingConfig.
//
// CountTenantPricePlans 实现 store.PricingConfig。
func (s *Store) CountTenantPricePlans(_ context.Context) (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int64{}
	for _, row := range s.pricingConfig.tenantPlans {
		if row.ValidTo == nil {
			out[row.PlanID]++
		}
	}
	return out, nil
}

// PricingConfigVersion implements store.PricingConfig.
//
// PricingConfigVersion 实现 store.PricingConfig。
func (s *Store) PricingConfigVersion(_ context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pricingConfig.version, nil
}
