package gormstore

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// MigrateBeforePartitionsForTest applies every embedded migration up to (not
// including) each namespace's partition conversion and records them in the
// ledger, reproducing a database from the release before partitioning so an
// upgrade test can load history into it first.
//
// MigrateBeforePartitionsForTest 应用每个分组中分区转换之前的全部内嵌迁移并写入账本，
// 重现分区之前那个版本的数据库，使升级测试可以先往里写入历史数据。
func MigrateBeforePartitionsForTest(ctx context.Context, s *Store) error {
	return s.withMigrationLock(ctx, func(db *gorm.DB) error {
		for _, namespace := range migrationNamespaces(db.Name()) {
			all, err := loadMigrations(namespace, db.Name())
			if err != nil {
				return err
			}
			table := "schema_migrations_" + namespace
			if err := ensureMigrationTable(db, table); err != nil {
				return err
			}
			for _, m := range all {
				if isPartitionMigration(m) {
					break
				}
				if err := executeMigration(db, namespace, m); err != nil {
					return err
				}
				record := migrationRecord{ID: m.id, Checksum: migrationChecksum(m.sql), AppliedAt: time.Now().UTC()}
				if err := db.Table(table).Create(&record).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// PartitionLayoutForTest reports a table's partitioning as the catalog sees it.
//
// PartitionLayoutForTest 报告数据库目录中一张表的分区情况。
func PartitionLayoutForTest(db *gorm.DB, table string) (column string, names []string, catchAll string, err error) {
	var layout partitionLayout
	read := func(tx *gorm.DB) error {
		var err error
		layout, err = readPartitionLayout(tx, table)
		return err
	}
	if db.Name() == "postgres" {
		err = withPostgresPartitionTx(db, read)
	} else {
		err = read(db)
	}
	if err != nil {
		return "", nil, "", err
	}
	for _, p := range layout.parts {
		if p.catchAll {
			catchAll = p.name
			continue
		}
		names = append(names, p.name)
	}
	return layout.column, names, catchAll, nil
}

// SetPartitionTuningForTest shrinks the delete batch and the periodic
// catch-all move limit, and pins the clock migrations use, returning a func
// that restores them.
//
// SetPartitionTuningForTest 调小删除批量与定时维护的兜底分区搬移上限，并固定迁移所用的
// 时钟；返回恢复原值的函数。
func SetPartitionTuningForTest(batch int, moveLimit int64, now func() time.Time) func() {
	oldBatch, oldLimit, oldNow := deleteBatchSize, maxCatchAllMove, partitionMigrationNow
	if batch > 0 {
		deleteBatchSize = batch
	}
	if moveLimit > 0 {
		maxCatchAllMove = moveLimit
	}
	if now != nil {
		partitionMigrationNow = now
	}
	return func() { deleteBatchSize, maxCatchAllMove, partitionMigrationNow = oldBatch, oldLimit, oldNow }
}
