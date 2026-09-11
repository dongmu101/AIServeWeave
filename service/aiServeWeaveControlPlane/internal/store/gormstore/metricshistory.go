package gormstore

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// UpsertRollup implements store.MetricsHistory.
func (s *Store) UpsertRollup(ctx context.Context, points []model.MetricsHistoryPoint) error {
	if len(points) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "metric"}, {Name: "labels"}, {Name: "bucket_at"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&points).Error
}

// ListRollup implements store.MetricsHistory.
func (s *Store) ListRollup(ctx context.Context, metrics []string, since, until time.Time) ([]model.MetricsHistoryPoint, error) {
	var points []model.MetricsHistoryPoint
	err := s.db.WithContext(ctx).
		Where("metric IN ? AND bucket_at >= ? AND bucket_at < ?", metrics, since, until).
		Order("bucket_at ASC").
		Find(&points).Error
	return points, err
}

// DeleteRollupBefore implements store.MetricsHistory.
func (s *Store) DeleteRollupBefore(ctx context.Context, before time.Time) (int64, error) {
	res := s.db.WithContext(ctx).Where("bucket_at < ?", before).Delete(&model.MetricsHistoryPoint{})
	return res.RowsAffected, res.Error
}
