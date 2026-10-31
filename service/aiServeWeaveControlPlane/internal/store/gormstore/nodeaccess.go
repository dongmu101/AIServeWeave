package gormstore

import (
	"context"

	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// ListNodeAccess reads every dedicated node's row, ordered by node id.
//
// ListNodeAccess 按节点 id 顺序读取每台专属节点的行。
func (s *Store) ListNodeAccess(ctx context.Context) ([]model.NodeAccess, error) {
	rows := []model.NodeAccess{}
	err := s.db.WithContext(ctx).Order("node_id").Find(&rows).Error
	return rows, translate(err)
}

// PutNodeAccess inserts or replaces one node's row as a single upsert, so two
// operators saving the same node at once leave one of their lists rather than
// a duplicate-key error.
//
// PutNodeAccess 以一次 upsert 插入或替换某台节点的行，使两位运维同时保存同一台节点
// 时，留下的是其中一份列表，而不是一个主键冲突错误。
func (s *Store) PutNodeAccess(ctx context.Context, row *model.NodeAccess) error {
	return translate(s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "node_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"tenants_json", "updated_by", "updated_at"}),
	}).Create(row).Error)
}

// DeleteNodeAccess removes one node's row, if there is one.
//
// DeleteNodeAccess 删除某台节点的行（如果存在）。
func (s *Store) DeleteNodeAccess(ctx context.Context, nodeID string) error {
	return translate(s.db.WithContext(ctx).Where("node_id = ?", nodeID).Delete(&model.NodeAccess{}).Error)
}
