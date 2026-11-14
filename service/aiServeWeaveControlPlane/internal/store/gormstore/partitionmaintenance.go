package gormstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// MaintainPartitions implements store.PartitionMaintenance. It creates every
// partitioned table's missing regular partitions up to its horizon. The
// migration lock is taken without waiting: when another replica (or a
// migration) holds it, the pass is skipped, not failed.
//
// MaintainPartitions 实现 store.PartitionMaintenance。它为每张分区表补建缺失的常规
// 分区，直到预建上界。迁移锁以不等待的方式获取：另一个副本（或迁移）持有锁时，本轮
// 跳过而不是失败。
func (s *Store) MaintainPartitions(ctx context.Context, now time.Time) (store.PartitionMaintenanceResult, error) {
	var result store.PartitionMaintenanceResult
	err := s.withSessionLock(ctx, func(db *gorm.DB) error {
		var errs []error
		for _, table := range partitionedTables {
			created, err := maintainTablePartitions(db, table, now)
			result.Created += created
			if err != nil {
				errs = append(errs, fmt.Errorf("maintaining partitions of %s: %w", table.name, err))
			}
		}
		return errors.Join(errs...)
	})
	if errors.Is(err, errMigrationLockBusy) {
		return store.PartitionMaintenanceResult{Skipped: true}, nil
	}
	return result, err
}

func maintainTablePartitions(db *gorm.DB, table partitionedTable, now time.Time) (int, error) {
	if db.Name() == "mysql" {
		layout, err := readPartitionLayout(db, table.name)
		if err != nil {
			return 0, err
		}
		catchAll, ok := catchAllPartition(layout.parts)
		if !layout.partitioned || !ok {
			return 0, errors.New("table is not partitioned with a catch-all partition")
		}
		missing := table.missingRanges(layout.parts, now)
		if len(missing) == 0 {
			return 0, nil
		}
		// REORGANIZE splits the catch-all; while it is empty (the normal case,
		// since partitions are created ahead) this only creates empty
		// partitions. Rows already in it are moved to where they belong.
		// REORGANIZE 拆分兜底分区；兜底分区为空时（正常情况，分区总是提前建好）只会建出
		// 空分区。其中已有的行会被搬到所属分区。
		defs := append(mysqlRangeDefs(table, missing), "PARTITION "+catchAll+" VALUES LESS THAN (MAXVALUE)")
		err = withMySQLLockWait(db, func() error {
			return db.Exec("ALTER TABLE " + table.name + " REORGANIZE PARTITION " + catchAll + " INTO (" + strings.Join(defs, ", ") + ")").Error
		})
		if err != nil {
			return 0, errors.New("reorganizing the catch-all partition failed")
		}
		return len(missing), nil
	}
	created := 0
	err := withPostgresPartitionTx(db, func(tx *gorm.DB) error {
		layout, err := readPartitionLayout(tx, table.name)
		if err != nil {
			return err
		}
		def, ok := catchAllPartition(layout.parts)
		if !layout.partitioned || !ok {
			return errors.New("table is not partitioned with a default partition")
		}
		missing := table.missingRanges(layout.parts, now)
		if len(missing) == 0 {
			return nil
		}
		from, to := missing[0].from, missing[len(missing)-1].to
		var moving bool
		if err := tx.Raw("SELECT EXISTS (SELECT 1 FROM "+def+" WHERE "+table.column+" >= ? AND "+table.column+" < ?)", from, to).Scan(&moving).Error; err != nil {
			return errors.New("inspecting the default partition failed")
		}
		// A new partition cannot be created while the default partition holds
		// rows in its range, so those rows are moved through a detached default.
		// 默认分区中仍有落在新分区范围内的行时无法建分区，因此先摘下默认分区再搬移这些行。
		if moving {
			if err := tx.Exec("ALTER TABLE " + table.name + " DETACH PARTITION " + def).Error; err != nil {
				return errors.New("detaching the default partition failed")
			}
		}
		for _, r := range missing {
			if err := tx.Exec(postgresCreatePartition(table, r)).Error; err != nil {
				return errors.New("creating a partition failed")
			}
		}
		if moving {
			move := "WITH moved AS (DELETE FROM " + def + " WHERE " + table.column + " >= ? AND " + table.column + " < ? RETURNING *) INSERT INTO " + table.name + " SELECT * FROM moved"
			if err := tx.Exec(move, from, to).Error; err != nil {
				return errors.New("moving rows out of the default partition failed")
			}
			if err := tx.Exec("ALTER TABLE " + table.name + " ATTACH PARTITION " + def + " DEFAULT").Error; err != nil {
				return errors.New("re-attaching the default partition failed")
			}
		}
		created = len(missing)
		return nil
	})
	return created, err
}

// dropExpiredPartitions drops every regular partition of table lying wholly
// before cutoff. A busy lock means another session is doing partition DDL; the
// caller's row delete still enforces the cutoff, so it is reported as zero.
//
// dropExpiredPartitions 删除 table 中整个区间都早于 cutoff 的常规分区。锁被占用说明
// 另一个会话正在执行分区 DDL；调用方随后的行删除仍会保证截止时间，因此记为零。
func (s *Store) dropExpiredPartitions(ctx context.Context, table partitionedTable, cutoff time.Time) (int64, error) {
	var dropped int64
	err := s.withSessionLock(ctx, func(db *gorm.DB) error {
		if db.Name() == "mysql" {
			layout, err := readPartitionLayout(db, table.name)
			if err != nil {
				return err
			}
			names := expiredPartitions(layout.parts, cutoff)
			if len(names) == 0 {
				return nil
			}
			err = withMySQLLockWait(db, func() error {
				return db.Exec("ALTER TABLE " + table.name + " DROP PARTITION " + strings.Join(names, ", ")).Error
			})
			if err != nil {
				return fmt.Errorf("dropping expired partitions of %s failed", table.name)
			}
			dropped = int64(len(names))
			return nil
		}
		return withPostgresPartitionTx(db, func(tx *gorm.DB) error {
			layout, err := readPartitionLayout(tx, table.name)
			if err != nil {
				return err
			}
			names := expiredPartitions(layout.parts, cutoff)
			if len(names) == 0 {
				return nil
			}
			if err := tx.Exec("DROP TABLE " + strings.Join(names, ", ")).Error; err != nil {
				return fmt.Errorf("dropping expired partitions of %s failed", table.name)
			}
			dropped = int64(len(names))
			return nil
		})
	})
	if errors.Is(err, errMigrationLockBusy) {
		return 0, nil
	}
	return dropped, err
}

// applyRetention drops table's wholly expired partitions, then deletes the
// remaining rows before cutoff (partition pruning confines this to the
// partition straddling the cutoff). A failed drop skips the delete, so a
// contended metadata lock never turns into one huge row delete; the next pass
// retries.
//
// applyRetention 先删除整段过期的分区，再删除截止时间之前剩下的行（分区裁剪使其只触达
// 跨越截止时间的分区）。删除分区失败时跳过行删除，避免元数据锁争用演变成一次巨大的
// 行删除；下一轮会重试。
func (s *Store) applyRetention(ctx context.Context, tableName string, value any, cutoff time.Time) (store.RetentionResult, error) {
	table, ok := lookupPartitionedTable(tableName)
	if !ok {
		return store.RetentionResult{}, fmt.Errorf("%s is not a partitioned table", tableName)
	}
	dropped, err := s.dropExpiredPartitions(ctx, table, cutoff)
	if err != nil {
		return store.RetentionResult{}, err
	}
	res := s.db.WithContext(ctx).Where(table.column+" < ?", cutoff).Delete(value)
	if res.Error != nil {
		return store.RetentionResult{PartitionsDropped: dropped}, res.Error
	}
	return store.RetentionResult{PartitionsDropped: dropped, RowsDeleted: res.RowsAffected}, nil
}

// withMySQLLockWait bounds metadata-lock waits for partition DDL on the pinned
// session and restores the server default afterwards, since the connection
// returns to the pool.
//
// withMySQLLockWait 在固定的会话上限制分区 DDL 等待元数据锁的时长，结束后恢复服务器
// 默认值，因为这个连接会回到连接池。
func withMySQLLockWait(db *gorm.DB, run func() error) error {
	if err := db.Exec(fmt.Sprintf("SET SESSION lock_wait_timeout = %d", int(partitionLockWait.Seconds()))).Error; err != nil {
		return err
	}
	defer db.Exec("SET SESSION lock_wait_timeout = DEFAULT")
	return run()
}

// withPostgresPartitionTx runs partition DDL in one transaction with a bounded
// lock wait and a UTC session time zone, so catalog bounds read back in UTC.
//
// withPostgresPartitionTx 在一个事务中执行分区 DDL，限制锁等待时长，并把会话时区设为
// UTC，使从目录读回的边界都是 UTC。
func withPostgresPartitionTx(db *gorm.DB, run func(*gorm.DB) error) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(fmt.Sprintf("SET LOCAL lock_timeout = '%dms'", partitionLockWait.Milliseconds())).Error; err != nil {
			return err
		}
		if err := tx.Exec("SET LOCAL TIME ZONE 'UTC'").Error; err != nil {
			return err
		}
		return run(tx)
	})
}
