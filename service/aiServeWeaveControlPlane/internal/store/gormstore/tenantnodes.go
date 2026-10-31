package gormstore

import (
	"context"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreateTenantNode implements store.TenantNodes.
//
// CreateTenantNode 实现 store.TenantNodes。
func (s *Store) CreateTenantNode(ctx context.Context, row *model.TenantNode) error {
	return translate(s.db.WithContext(ctx).Create(row).Error)
}

// GetTenantNode implements store.TenantNodes.
//
// GetTenantNode 实现 store.TenantNodes。
func (s *Store) GetTenantNode(ctx context.Context, nodeID string) (model.TenantNode, error) {
	var row model.TenantNode
	err := s.db.WithContext(ctx).Where("node_id = ?", nodeID).Take(&row).Error
	return row, translate(err)
}

// ListTenantNodes implements store.TenantNodes over the (tenant_id,
// created_at) index.
//
// ListTenantNodes 基于 (tenant_id, created_at) 索引实现 store.TenantNodes。
func (s *Store) ListTenantNodes(ctx context.Context, tenantID string, limit int) ([]model.TenantNode, error) {
	rows := []model.TenantNode{}
	err := s.db.WithContext(ctx).Where("tenant_id = ?", tenantID).
		Order("created_at DESC").Order("node_id DESC").Limit(limit).Find(&rows).Error
	return rows, translate(err)
}

// CountTenantNodes implements store.TenantNodes.
//
// CountTenantNodes 实现 store.TenantNodes。
func (s *Store) CountTenantNodes(ctx context.Context, tenantID string, since time.Time) (active, created int64, err error) {
	db := s.db.WithContext(ctx).Model(&model.TenantNode{})
	if err := db.Where("tenant_id = ? AND revoked_at IS NULL", tenantID).Count(&active).Error; err != nil {
		return 0, 0, translate(err)
	}
	db = s.db.WithContext(ctx).Model(&model.TenantNode{})
	if err := db.Where("tenant_id = ? AND created_at >= ?", tenantID, since).Count(&created).Error; err != nil {
		return 0, 0, translate(err)
	}
	return active, created, nil
}

// RevokeTenantNode implements store.TenantNodes with one conditional update,
// so two concurrent deletions mark the row once and the second sees
// ErrNotFound.
//
// RevokeTenantNode 以一次条件更新实现 store.TenantNodes，使两次并发删除只标记一次，
// 第二次看到 ErrNotFound。
func (s *Store) RevokeTenantNode(ctx context.Context, tenantID, nodeID, by string, at time.Time) error {
	res := s.db.WithContext(ctx).Model(&model.TenantNode{}).
		Where("node_id = ? AND tenant_id = ? AND revoked_at IS NULL", nodeID, tenantID).
		Updates(map[string]any{"revoked_at": at, "revoked_by": by})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// DeleteTenantNode implements store.TenantNodes.
//
// DeleteTenantNode 实现 store.TenantNodes。
func (s *Store) DeleteTenantNode(ctx context.Context, nodeID string) error {
	return translate(s.db.WithContext(ctx).Where("node_id = ?", nodeID).Delete(&model.TenantNode{}).Error)
}
