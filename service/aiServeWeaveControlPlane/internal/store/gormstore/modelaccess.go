package gormstore

import (
	"context"

	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// GetModelAccess reads the rows stored under scopes in one statement.
//
// GetModelAccess 用一条语句读取 scopes 下存储的行。
func (s *Store) GetModelAccess(ctx context.Context, scopes ...string) ([]model.ModelAccess, error) {
	rows := []model.ModelAccess{}
	if len(scopes) == 0 {
		return rows, nil
	}
	err := s.db.WithContext(ctx).Where("scope IN ?", scopes).Find(&rows).Error
	return rows, translate(err)
}

// PutModelAccess inserts or replaces one scope's row as a single upsert, so
// two operators saving the same scope at once leave one of their lists rather
// than a duplicate-key error.
//
// PutModelAccess 以一次 upsert 插入或替换某个 scope 的行，使两位运维同时保存同一个
// scope 时，留下的是其中一份列表，而不是一个主键冲突错误。
func (s *Store) PutModelAccess(ctx context.Context, row *model.ModelAccess) error {
	return translate(s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope"}},
		DoUpdates: clause.AssignmentColumns([]string{"models_json", "updated_by", "updated_at"}),
	}).Create(row).Error)
}

// DeleteModelAccess removes one scope's row, if there is one.
//
// DeleteModelAccess 删除某个 scope 的行（如果存在）。
func (s *Store) DeleteModelAccess(ctx context.Context, scope string) error {
	return translate(s.db.WithContext(ctx).Where("scope = ?", scope).Delete(&model.ModelAccess{}).Error)
}
