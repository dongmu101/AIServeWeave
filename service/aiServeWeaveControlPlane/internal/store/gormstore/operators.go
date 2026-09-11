package gormstore

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreatePlatformOperatorWithAudit inserts an operator and audit in one
// transaction.
//
// CreatePlatformOperatorWithAudit 在一个事务中插入运维账户与审计。
func (s *Store) CreatePlatformOperatorWithAudit(ctx context.Context, operator *model.PlatformOperator, audit model.AuditLog) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(operator).Error; err != nil {
			return err
		}
		return tx.Create(&audit).Error
	}))
}

// GetPlatformOperator reads one platform operator by id.
//
// GetPlatformOperator 按 id 读取一个平台运维账户。
func (s *Store) GetPlatformOperator(ctx context.Context, id string) (model.PlatformOperator, error) {
	var operator model.PlatformOperator
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&operator).Error
	return operator, translate(err)
}

// ListPlatformOperators reads platform operators newest first.
//
// ListPlatformOperators 读取平台运维账户，最新的在前。
func (s *Store) ListPlatformOperators(ctx context.Context, query store.ListQuery, filter store.PlatformOperatorFilter) (store.Page[model.PlatformOperator], error) {
	db := s.db.WithContext(ctx).Model(&model.PlatformOperator{})
	if filter.Status != "" {
		db = db.Where("status = ?", filter.Status)
	}
	if filter.Query != "" {
		like := likePattern(filter.Query)
		db = db.Where("LOWER(email) LIKE ? OR LOWER(name) LIKE ?", like, like)
	}
	return readPage(db, query, func(operator model.PlatformOperator) (time.Time, string) { return operator.CreatedAt, operator.ID })
}

// UpdatePlatformOperatorPassword changes one operator digest and appends its
// audit in one transaction.
//
// UpdatePlatformOperatorPassword 在一个事务中修改运维账户摘要并追加审计。
func (s *Store) UpdatePlatformOperatorPassword(ctx context.Context, id, passwordHash string, audit model.AuditLog) (model.PlatformOperator, error) {
	var operator model.PlatformOperator
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&operator).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.PlatformOperator{}).Where("id = ?", id).Updates(map[string]any{
			"password_hash": passwordHash, "updated_at": audit.CreatedAt,
		}).Error; err != nil {
			return err
		}
		operator.PasswordHash = passwordHash
		operator.UpdatedAt = audit.CreatedAt
		return tx.Create(&audit).Error
	})
	return operator, translate(err)
}

// SetPlatformOperatorStatus changes one status while transactionally
// preserving an active platform operator.
//
// SetPlatformOperatorStatus 修改一个状态，并在事务中保留一名有效平台运维。
func (s *Store) SetPlatformOperatorStatus(ctx context.Context, id, status string, at time.Time, audit model.AuditLog) (store.PlatformOperatorLifecycleResult, error) {
	var result store.PlatformOperatorLifecycleResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// One deterministic active row serializes status changes without loading
		// the unbounded operator set.
		//
		// 一条确定的有效记录让状态变更串行化，无需装载无界运维集合。
		var anchor model.PlatformOperator
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("status = ?", model.StatusActive).Order("id").Take(&anchor).Error; err != nil {
			return err
		}
		var active int64
		if err := tx.Model(&model.PlatformOperator{}).Where("status = ?", model.StatusActive).Count(&active).Error; err != nil {
			return err
		}
		var operator model.PlatformOperator
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&operator).Error; err != nil {
			return err
		}
		if operator.Status == status {
			result = store.PlatformOperatorLifecycleResult{Operator: operator}
			return nil
		}
		if status == model.StatusSuspended && operator.Status == model.StatusActive && active <= 1 {
			return store.ErrConflict
		}
		if err := tx.Model(&model.PlatformOperator{}).Where("id = ?", id).Updates(map[string]any{
			"status": status, "updated_at": at,
		}).Error; err != nil {
			return err
		}
		operator.Status = status
		operator.UpdatedAt = at
		if err := tx.Create(&audit).Error; err != nil {
			return err
		}
		result = store.PlatformOperatorLifecycleResult{Operator: operator, Changed: true}
		return nil
	})
	return result, translate(err)
}
