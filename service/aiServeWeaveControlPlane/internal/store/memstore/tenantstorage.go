package memstore

import (
	"context"
	"sort"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// GetTenantStorage implements store.TenantStorages.
//
// GetTenantStorage 实现 store.TenantStorages。
func (s *Store) GetTenantStorage(_ context.Context, tenantID string) (model.TenantStorage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.tenantStorage[tenantID]
	if !ok {
		return model.TenantStorage{}, store.ErrNotFound
	}
	return row, nil
}

// PutTenantStorage implements store.TenantStorages.
//
// PutTenantStorage 实现 store.TenantStorages。
func (s *Store) PutTenantStorage(_ context.Context, row *model.TenantStorage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tenantStorage == nil {
		s.tenantStorage = map[string]model.TenantStorage{}
	}
	row.Version = 1
	if existing, ok := s.tenantStorage[row.TenantID]; ok {
		row.CreatedAt = existing.CreatedAt
		row.Version = existing.Version + 1
	}
	s.tenantStorage[row.TenantID] = *row
	return nil
}

// DeleteTenantStorage implements store.TenantStorages.
//
// DeleteTenantStorage 实现 store.TenantStorages。
func (s *Store) DeleteTenantStorage(_ context.Context, tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tenantStorage[tenantID]; !ok {
		return store.ErrNotFound
	}
	delete(s.tenantStorage, tenantID)
	return nil
}

// ListRetainedTenantStorage implements store.TenantStorages.
//
// ListRetainedTenantStorage 实现 store.TenantStorages。
func (s *Store) ListRetainedTenantStorage(_ context.Context, after string, limit int) ([]model.TenantStorage, error) {
	if limit <= 0 || limit > store.MaxRetainedTenantStorage {
		limit = store.MaxRetainedTenantStorage
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := []model.TenantStorage{}
	for id, row := range s.tenantStorage {
		if row.RetentionDays > 0 && id > after {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].TenantID < rows[j].TenantID })
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}
