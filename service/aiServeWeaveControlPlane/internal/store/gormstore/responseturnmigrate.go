package gormstore

import "context"

// MigrateResponseTurns applies the response_turns versioned SQL under the
// shared migration lock. It exists as a narrow, single-namespace entry point
// for live tests — production startup goes through MigrateAll, which already
// includes "response_turns" via migrationNamespaces.
//
// MigrateResponseTurns 在共享迁移锁下应用 response_turns 的版本化 SQL。它是
// 供独立测试使用的窄入口——生产启动走 MigrateAll，其中已经通过
// migrationNamespaces 包含了 "response_turns"。
func (s *Store) MigrateResponseTurns(ctx context.Context) ([]string, error) {
	return s.migrateOne(ctx, "response_turns")
}
