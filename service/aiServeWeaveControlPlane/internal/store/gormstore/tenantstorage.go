package gormstore

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// GetTenantStorage implements store.TenantStorages.
//
// GetTenantStorage 实现 store.TenantStorages。
func (s *Store) GetTenantStorage(ctx context.Context, tenantID string) (model.TenantStorage, error) {
	var row model.TenantStorage
	err := s.db.WithContext(ctx).Where("tenant_id = ?", tenantID).Take(&row).Error
	return row, translate(err)
}

// PutTenantStorage implements store.TenantStorages. It is one upsert that
// bumps the version in the database, so concurrent saves for one tenant
// serialise on the row, each gets its own version, and two first saves do
// not deadlock on a lock of a row that does not exist yet (MySQL takes a gap
// lock for that). The saved version and creation time are read back in the
// same transaction.
//
// PutTenantStorage 实现 store.TenantStorages。它是一条在数据库里递增版本号的 upsert，
// 因此同一租户的并发保存在该行上串行、各得一个版本，两次首次保存也不会因为锁一个尚不存在
// 的行而死锁（MySQL 为此会加间隙锁）。保存后的版本号与创建时间在同一事务中读回。
func (s *Store) PutTenantStorage(ctx context.Context, row *model.TenantStorage) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		insert := *row
		insert.Version = 1
		err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "tenant_id"}},
			DoUpdates: append(clause.AssignmentColumns([]string{
				"kind", "endpoint", "region", "bucket", "prefix", "path_style", "username",
				"secret_ciphertext", "secret_hint", "retention_days", "updated_by", "updated_at",
			}), clause.Assignment{Column: clause.Column{Name: "version"}, Value: gorm.Expr("tenant_storage.version + 1")}),
		}).Create(&insert).Error
		if err != nil {
			return err
		}
		var saved model.TenantStorage
		if err := tx.Select("version", "created_at").Where("tenant_id = ?", row.TenantID).Take(&saved).Error; err != nil {
			return err
		}
		row.Version, row.CreatedAt = saved.Version, saved.CreatedAt
		return nil
	})
	return translate(err)
}

// DeleteTenantStorage implements store.TenantStorages.
//
// DeleteTenantStorage 实现 store.TenantStorages。
func (s *Store) DeleteTenantStorage(ctx context.Context, tenantID string) error {
	res := s.db.WithContext(ctx).Where("tenant_id = ?", tenantID).Delete(&model.TenantStorage{})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ListRetainedTenantStorage implements store.TenantStorages.
//
// ListRetainedTenantStorage 实现 store.TenantStorages。
func (s *Store) ListRetainedTenantStorage(ctx context.Context, after string, limit int) ([]model.TenantStorage, error) {
	if limit <= 0 || limit > store.MaxRetainedTenantStorage {
		limit = store.MaxRetainedTenantStorage
	}
	rows := []model.TenantStorage{}
	err := s.db.WithContext(ctx).
		Where("retention_days > 0 AND tenant_id > ?", after).
		Order("tenant_id").Limit(limit).Find(&rows).Error
	return rows, translate(err)
}
