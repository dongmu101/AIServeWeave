package gormstore

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// GetArkCredential implements store.ArkCredentials.
//
// GetArkCredential 实现 store.ArkCredentials。
func (s *Store) GetArkCredential(ctx context.Context, scope string) (model.ArkCredential, error) {
	var row model.ArkCredential
	err := s.db.WithContext(ctx).Where("scope = ?", scope).Take(&row).Error
	return row, translate(err)
}

// PutArkCredential implements store.ArkCredentials. Like PutTenantStorage it
// is one upsert that bumps the version in the database, so concurrent saves
// for one scope serialise on the row and two first saves do not deadlock on
// a gap lock; the saved version and creation time are read back in the same
// transaction.
//
// PutArkCredential 实现 store.ArkCredentials。与 PutTenantStorage 一样，它是一条在数据库里
// 递增版本号的 upsert，因此同一 scope 的并发保存在该行上串行，两次首次保存也不会因间隙锁而
// 死锁；保存后的版本号与创建时间在同一事务中读回。
func (s *Store) PutArkCredential(ctx context.Context, row *model.ArkCredential) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		insert := *row
		insert.Version = 1
		err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "scope"}},
			DoUpdates: append(clause.AssignmentColumns([]string{
				"key_ciphertext", "key_last4", "base_url", "models", "updated_by", "updated_at",
			}), clause.Assignment{Column: clause.Column{Name: "version"}, Value: gorm.Expr("ark_credentials.version + 1")}),
		}).Create(&insert).Error
		if err != nil {
			return err
		}
		var saved model.ArkCredential
		if err := tx.Select("version", "created_at").Where("scope = ?", row.Scope).Take(&saved).Error; err != nil {
			return err
		}
		row.Version, row.CreatedAt = saved.Version, saved.CreatedAt
		return nil
	})
	return translate(err)
}

// DeleteArkCredential implements store.ArkCredentials.
//
// DeleteArkCredential 实现 store.ArkCredentials。
func (s *Store) DeleteArkCredential(ctx context.Context, scope string) error {
	res := s.db.WithContext(ctx).Where("scope = ?", scope).Delete(&model.ArkCredential{})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}
