package gormstore

import "context"

// MigrateMetricsHistory applies versioned metrics_history SQL under the
// shared migration lock.
//
// MigrateMetricsHistory 在共享迁移锁下应用 metrics_history 的版本化 SQL。
func (s *Store) MigrateMetricsHistory(ctx context.Context) ([]string, error) {
	return s.migrateOne(ctx, "metrics_history")
}
