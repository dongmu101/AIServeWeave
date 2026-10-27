package gormstore

import "context"

// MigrateUsageRecords applies the usage_records versioned SQL under the
// shared migration lock. It exists as a narrow, single-namespace entry
// point for live tests — production startup goes through MigrateAll, which
// already includes "usage_records" via migrationNamespaces.
//
// MigrateUsageRecords 在共享迁移锁下应用 usage_records 的版本化 SQL。它是
// 供独立测试使用的窄入口——生产启动走 MigrateAll，其中已经通过
// migrationNamespaces 包含了 "usage_records"。
func (s *Store) MigrateUsageRecords(ctx context.Context) ([]string, error) {
	return s.migrateOne(ctx, "usage_records")
}
