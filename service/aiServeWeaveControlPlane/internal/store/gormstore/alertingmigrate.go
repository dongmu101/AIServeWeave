package gormstore

import "context"

// MigrateAlerting applies the alerting versioned SQL (alert_rules,
// alert_instances) under the shared migration lock. A narrow,
// single-namespace entry point for live tests — production startup goes
// through MigrateAll, which already includes "alerting" via
// migrationNamespaces.
//
// MigrateAlerting 在共享迁移锁下应用 alerting 的版本化 SQL（alert_rules、
// alert_instances）。这是供独立测试使用的窄入口——生产启动走 MigrateAll，
// 其中已经通过 migrationNamespaces 包含了 "alerting"。
func (s *Store) MigrateAlerting(ctx context.Context) ([]string, error) {
	return s.migrateOne(ctx, "alerting")
}
