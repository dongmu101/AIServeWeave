package memstore

import (
	"context"
	"sort"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreateTenantNode implements store.TenantNodes.
//
// CreateTenantNode 实现 store.TenantNodes。
func (s *Store) CreateTenantNode(_ context.Context, row *model.TenantNode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tenantNodes[row.NodeID]; exists {
		return store.ErrConflict
	}
	s.tenantNodes[row.NodeID] = *row
	return nil
}

// GetTenantNode implements store.TenantNodes.
//
// GetTenantNode 实现 store.TenantNodes。
func (s *Store) GetTenantNode(_ context.Context, nodeID string) (model.TenantNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.tenantNodes[nodeID]
	if !ok {
		return model.TenantNode{}, store.ErrNotFound
	}
	return row, nil
}

// ListTenantNodes implements store.TenantNodes.
//
// ListTenantNodes 实现 store.TenantNodes。
func (s *Store) ListTenantNodes(_ context.Context, tenantID string, limit int) ([]model.TenantNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := []model.TenantNode{}
	for _, row := range s.tenantNodes {
		if row.TenantID == tenantID {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].CreatedAt.After(rows[j].CreatedAt)
		}
		return rows[i].NodeID > rows[j].NodeID
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

// CountTenantNodes implements store.TenantNodes.
//
// CountTenantNodes 实现 store.TenantNodes。
func (s *Store) CountTenantNodes(_ context.Context, tenantID string, since time.Time) (active, created int64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.tenantNodes {
		if row.TenantID != tenantID {
			continue
		}
		if row.RevokedAt == nil {
			active++
		}
		if !row.CreatedAt.Before(since) {
			created++
		}
	}
	return active, created, nil
}

// RevokeTenantNode implements store.TenantNodes.
//
// RevokeTenantNode 实现 store.TenantNodes。
func (s *Store) RevokeTenantNode(_ context.Context, tenantID, nodeID, by string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.tenantNodes[nodeID]
	if !ok || row.TenantID != tenantID || row.RevokedAt != nil {
		return store.ErrNotFound
	}
	row.RevokedAt, row.RevokedBy = &at, by
	s.tenantNodes[nodeID] = row
	return nil
}

// DeleteTenantNode implements store.TenantNodes.
//
// DeleteTenantNode 实现 store.TenantNodes。
func (s *Store) DeleteTenantNode(_ context.Context, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tenantNodes, nodeID)
	return nil
}
