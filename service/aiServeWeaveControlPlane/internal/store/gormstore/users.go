package gormstore

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// GetUser reads one user by id, scoped to its tenant.
//
// GetUser 按 id 读取一个用户，并限定在其租户范围内。
func (s *Store) GetUser(ctx context.Context, tenantID, id string) (model.User, error) {
	var user model.User
	err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).Take(&user).Error
	return user, translate(err)
}

// UpdateUserPassword changes a scoped user's digest and appends the supplied
// audit entry in one transaction.
//
// UpdateUserPassword 在一个事务中修改限定范围用户的摘要并追加指定审计。
func (s *Store) UpdateUserPassword(ctx context.Context, tenantID, id, passwordHash string, audit model.AuditLog) (model.User, error) {
	var user model.User
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND id = ?", tenantID, id).Take(&user).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.User{}).Where("tenant_id = ? AND id = ?", tenantID, id).Updates(map[string]any{
			"password_hash": passwordHash,
			"updated_at":    audit.CreatedAt,
		}).Error; err != nil {
			return err
		}
		user.PasswordHash = passwordHash
		user.UpdatedAt = audit.CreatedAt
		return tx.Create(&audit).Error
	})
	return user, translate(err)
}

// UpdateUserRole changes a scoped user's role while transactionally
// preserving at least one active owner.
//
// UpdateUserRole 修改限定范围用户的角色，并在事务中保留至少一个有效 owner。
func (s *Store) UpdateUserRole(ctx context.Context, tenantID, id, role string, at time.Time, audit model.AuditLog) (store.UserLifecycleResult, error) {
	return s.mutateUser(ctx, tenantID, id, at, audit, func(tx *gorm.DB, user *model.User, owners int64) (bool, error) {
		if user.Role == role {
			return false, nil
		}
		if user.Status == model.StatusActive && user.Role == model.RoleOwner && role != model.RoleOwner && owners <= 1 {
			return false, store.ErrConflict
		}
		if err := tx.Model(&model.User{}).Where("tenant_id = ? AND id = ?", tenantID, id).Updates(map[string]any{
			"role": role, "updated_at": at,
		}).Error; err != nil {
			return false, err
		}
		user.Role = role
		return true, nil
	})
}

// SetUserStatus changes a scoped user's status and revokes their active API
// Keys in the same transaction when suspending them.
//
// SetUserStatus 修改限定范围用户的状态，并在暂停时于同一事务中吊销其有效 API Key。
func (s *Store) SetUserStatus(ctx context.Context, tenantID, id, status string, at time.Time, audit model.AuditLog) (store.UserLifecycleResult, error) {
	return s.mutateUser(ctx, tenantID, id, at, audit, func(tx *gorm.DB, user *model.User, owners int64) (bool, error) {
		if user.Status == status {
			return false, nil
		}
		if status == model.StatusSuspended && user.Status == model.StatusActive && user.Role == model.RoleOwner && owners <= 1 {
			return false, store.ErrConflict
		}
		if err := tx.Model(&model.User{}).Where("tenant_id = ? AND id = ?", tenantID, id).Updates(map[string]any{
			"status": status, "updated_at": at,
		}).Error; err != nil {
			return false, err
		}
		if status == model.StatusSuspended {
			if err := tx.Model(&model.APIKey{}).
				Where("tenant_id = ? AND created_by = ? AND status = ?", tenantID, id, model.StatusActive).
				Updates(map[string]any{"status": model.StatusRevoked, "revoked_at": at, "updated_at": at}).Error; err != nil {
				return false, err
			}
			if err := enqueueRevocation(tx); err != nil {
				return false, err
			}
		}
		user.Status = status
		return true, nil
	})
}

func (s *Store) mutateUser(ctx context.Context, tenantID, id string, at time.Time, audit model.AuditLog, change func(*gorm.DB, *model.User, int64) (bool, error)) (store.UserLifecycleResult, error) {
	var result store.UserLifecycleResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The tenant row is the bounded serialization anchor for owner changes;
		// loading every owner merely to lock them would make one request unbounded.
		//
		// 租户行是 owner 变更的有界串行化锚点；仅为加锁而装载全部 owner，会让单次请求无界。
		var tenant model.Tenant
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id = ?", tenantID).Take(&tenant).Error; err != nil {
			return err
		}
		var owners int64
		if err := tx.Model(&model.User{}).Where("tenant_id = ? AND role = ? AND status = ?", tenantID, model.RoleOwner, model.StatusActive).Count(&owners).Error; err != nil {
			return err
		}
		var user model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND id = ?", tenantID, id).Take(&user).Error; err != nil {
			return err
		}
		changed, err := change(tx, &user, owners)
		if err != nil {
			return err
		}
		if !changed {
			result = store.UserLifecycleResult{User: user}
			return nil
		}
		user.UpdatedAt = at
		if err := tx.Create(&audit).Error; err != nil {
			return err
		}
		result = store.UserLifecycleResult{User: user, Changed: true}
		return nil
	})
	return result, translate(err)
}
