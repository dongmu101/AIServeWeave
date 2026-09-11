package gormstore

import "context"

// MigrateRoutes applies versioned routes SQL under the shared migration lock.
// MigrateRoutes 在共享迁移锁下应用路由的版本化 SQL。
func (s *Store) MigrateRoutes(ctx context.Context) ([]string, error) {
	return s.migrateOne(ctx, "routes")
}
