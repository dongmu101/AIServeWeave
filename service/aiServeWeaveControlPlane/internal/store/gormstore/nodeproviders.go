package gormstore

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// ListNodeProviders implements store.NodeProviders.
//
// ListNodeProviders 实现 store.NodeProviders。
func (s *Store) ListNodeProviders(ctx context.Context, nodeID string) ([]model.NodeProvider, error) {
	rows := []model.NodeProvider{}
	err := s.db.WithContext(ctx).Where("node_id = ?", nodeID).Order("runtime_id").Find(&rows).Error
	return rows, translate(err)
}

// PutNodeProvider implements store.NodeProviders. The version row is locked
// first, so concurrent changes to one node serialise on it.
//
// PutNodeProvider 实现 store.NodeProviders。先锁版本行，使同一节点的并发修改在它上面串行。
func (s *Store) PutNodeProvider(ctx context.Context, row *model.NodeProvider) (uint64, error) {
	var version uint64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		v, err := bumpNodeProviderVersion(tx, row.NodeID, row.UpdatedAt)
		if err != nil {
			return err
		}
		version = v
		var existing model.NodeProvider
		err = tx.Where("node_id = ? AND runtime_id = ?", row.NodeID, row.RuntimeID).Take(&existing).Error
		switch {
		case err == nil:
			row.CreatedAt = existing.CreatedAt
			return tx.Save(row).Error
		case errors.Is(err, gorm.ErrRecordNotFound):
			return tx.Create(row).Error
		default:
			return err
		}
	})
	return version, translate(err)
}

// DeleteNodeProvider implements store.NodeProviders.
//
// DeleteNodeProvider 实现 store.NodeProviders。
func (s *Store) DeleteNodeProvider(ctx context.Context, nodeID, runtimeID string, at time.Time) (uint64, error) {
	var version uint64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		v, err := bumpNodeProviderVersion(tx, nodeID, at)
		if err != nil {
			return err
		}
		version = v
		res := tx.Where("node_id = ? AND runtime_id = ?", nodeID, runtimeID).Delete(&model.NodeProvider{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return store.ErrNotFound
		}
		return nil
	})
	return version, translate(err)
}

// bumpNodeProviderVersion increments nodeID's version under a row lock,
// creating it at 1. Two first writes racing on the insert make one of them
// fail with ErrConflict.
//
// bumpNodeProviderVersion 在行锁下把 nodeID 的版本加一，不存在时以 1 创建。两次首写在
// 插入上竞争时，其中一次以 ErrConflict 失败。
func bumpNodeProviderVersion(tx *gorm.DB, nodeID string, at time.Time) (uint64, error) {
	var row model.NodeProviderVersion
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_id = ?", nodeID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		row = model.NodeProviderVersion{NodeID: nodeID, Version: 1, UpdatedAt: at}
		return 1, tx.Create(&row).Error
	}
	if err != nil {
		return 0, err
	}
	row.Version++
	row.UpdatedAt = at
	return row.Version, tx.Model(&model.NodeProviderVersion{}).Where("node_id = ?", nodeID).
		Updates(map[string]any{"version": row.Version, "updated_at": at}).Error
}

// NodeProviderVersion implements store.NodeProviders.
//
// NodeProviderVersion 实现 store.NodeProviders。
func (s *Store) NodeProviderVersion(ctx context.Context, nodeID string) (uint64, error) {
	var row model.NodeProviderVersion
	err := s.db.WithContext(ctx).Where("node_id = ?", nodeID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	return row.Version, translate(err)
}

// ListNodeProviderVersions implements store.NodeProviders.
//
// ListNodeProviderVersions 实现 store.NodeProviders。
func (s *Store) ListNodeProviderVersions(ctx context.Context) ([]model.NodeProviderVersion, error) {
	rows := []model.NodeProviderVersion{}
	err := s.db.WithContext(ctx).Order("node_id").Find(&rows).Error
	return rows, translate(err)
}
