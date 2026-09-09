package gormstore

import (
	"context"

	"gorm.io/gorm"

	"AIServeWeave/common/modelroute"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CurrentRouteRevision reads the active immutable row in one statement. / CurrentRouteRevision 用一条语句读取当前不可变版本。
func (s *Store) CurrentRouteRevision(ctx context.Context) (model.RouteRevision, error) {
	var row model.RouteRevision
	err := s.db.WithContext(ctx).Table("route_revisions").Select("route_revisions.*").Joins("JOIN route_actives ON route_actives.revision = route_revisions.revision AND route_actives.id = 1").Take(&row).Error
	return row, translate(err)
}

// GetRouteRevision reads an immutable historical row. / GetRouteRevision 读取不可变历史行。
func (s *Store) GetRouteRevision(ctx context.Context, revision int64) (model.RouteRevision, error) {
	var row model.RouteRevision
	err := s.db.WithContext(ctx).Where("revision = ?", revision).Take(&row).Error
	return row, translate(err)
}

// ListRouteRevisions reads bounded metadata without loading route documents. / ListRouteRevisions 有界读取元数据，不加载路由正文。
func (s *Store) ListRouteRevisions(ctx context.Context, before int64, limit int) ([]model.RouteRevision, error) {
	if limit < 1 || limit > 51 {
		limit = 51
	}
	q := s.db.WithContext(ctx).Select("revision, digest, actor_id, created_at, rollback_of").Order("revision DESC").Limit(limit)
	if before > 0 {
		q = q.Where("revision < ?", before)
	}
	rows := []model.RouteRevision{}
	err := q.Find(&rows).Error
	return rows, translate(err)
}

// PublishRouteRevision commits CAS, immutable history and audit in one transaction. / PublishRouteRevision 在同一事务中提交 CAS、不可变历史与审计。
func (s *Store) PublishRouteRevision(ctx context.Context, expected int64, row *model.RouteRevision, audit *model.AuditLog) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if expected < 0 {
			return store.ErrConflict
		}
		result := tx.Model(&model.RouteActive{}).Where("id = ? AND revision = ?", 1, expected).Update("revision", expected+1)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return store.ErrConflict
		}
		if expected >= modelroute.MaxRevisions {
			return store.ErrRouteCapacity
		}
		row.Revision = expected + 1
		if err := tx.Create(row).Error; err != nil {
			return err
		}
		return tx.Create(audit).Error
	}))
}
