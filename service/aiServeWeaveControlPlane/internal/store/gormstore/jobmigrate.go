package gormstore

import (
	"context"
	"errors"
)

// MigrateJobs applies the MySQL-only Job versions with checksum and dirty tracking.
// MigrateJobs 应用仅支持 MySQL 的 Job 版本，并记录校验和与 dirty 状态。
func (s *Store) MigrateJobs(ctx context.Context) ([]string, error) {
	if s.db.Name() != "mysql" {
		return nil, errors.New("gormstore: MigrateJobs requires mysql; Job persistence is MySQL-only per STATUS.md")
	}
	return s.migrateOne(ctx, "jobs")
}
