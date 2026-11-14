package gormstore

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
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
func (s *Store) DeleteRollupBefore(ctx context.Context, before time.Time) (store.RetentionResult, error) {
	return s.applyRetention(ctx, "metrics_history_points", &model.MetricsHistoryPoint{}, before)
}
