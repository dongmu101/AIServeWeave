package gormstore

import "context"

// MigrateRequestLogs applies the request_logs versioned SQL under the shared
// migration lock. It exists as a narrow, single-namespace entry point for
// live tests — production startup goes through MigrateAll, which already
// includes "request_logs" via migrationNamespaces.
//
// MigrateRequestLogs 在共享迁移锁下应用 request_logs 的版本化 SQL。它是供
// 独立测试使用的窄入口——生产启动走 MigrateAll，其中已经通过
// migrationNamespaces 包含了 "request_logs"。
func (s *Store) MigrateRequestLogs(ctx context.Context) ([]string, error) {
	return s.migrateOne(ctx, "request_logs")
}
