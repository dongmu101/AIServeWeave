package gormstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

var (
	// deleteBatchSize bounds one row-delete statement, so no retention pass
	// ever runs one huge transaction. A variable so tests can shrink it.
	// deleteBatchSize 限制单条行删除语句的行数，使保留期清理永远不会跑成一个巨大的事务。
	// 定义为变量以便测试调小。
	deleteBatchSize = 5000
	// maxCatchAllMove is the most rows the periodic maintenance moves out of a
	// catch-all partition. Moving holds a lock that blocks writes to the table,
	// so a larger backlog is left to the migration command, run in a quiet
	// window. A variable so tests can shrink it.
	// maxCatchAllMove 是定时维护从兜底分区搬出的最大行数。搬移期间持有阻塞该表写入的锁，
	// 更大的积压留给在低峰期执行的迁移命令。定义为变量以便测试调小。
	maxCatchAllMove int64 = 200_000
)

const (
	// retentionLockPatience is how long a retention pass waits for the
	// partition lock. Every sweeper and the maintenance loop tick together,
	// and each holds the lock only briefly, so waiting serialises them
	// instead of making all but one skip.
	// retentionLockPatience 是保留期清理等待分区锁的最长时间。各清理协程与维护循环同时
	// 触发，每个只短暂持锁，等待能让它们排队执行，而不是只有一个成功、其余全部跳过。
	retentionLockPatience = 2 * time.Minute
	lockPollInterval      = time.Second
)

// ddlDB is the connection pool partition DDL runs on: the maintenance pool
// when one is configured (a deployment whose runtime account has no DDL
// rights), otherwise the main pool.
//
// ddlDB 是执行分区 DDL 的连接池：配置了维护连接池时用它（运行账号没有 DDL 权限的部署），
// 否则用主连接池。
func (s *Store) ddlDB() *gorm.DB {
	if s.maintenance != nil {
		return s.maintenance
	}
	return s.db
}

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
	err := s.withSessionLockOn(ctx, s.ddlDB(), func(db *gorm.DB) error {
		var err error
		result.Created, err = maintainAllPartitions(db, now, maxCatchAllMove)
		return err
	})
	if errors.Is(err, errMigrationLockBusy) {
		return store.PartitionMaintenanceResult{Skipped: true}, nil
	}
	return result, err
}

// maintainAllPartitions runs maintenance for every partitioned table on a
// session that already holds the migration lock. moveLimit caps the rows moved
// out of a catch-all partition; zero means no cap (the migration command).
//
// maintainAllPartitions 在已持有迁移锁的会话上为每张分区表执行维护。moveLimit 限制从
// 兜底分区搬出的行数，为零表示不限（迁移命令使用）。
func maintainAllPartitions(db *gorm.DB, now time.Time, moveLimit int64) (int, error) {
	created := 0
	var errs []error
	for _, table := range partitionedTables {
		n, err := maintainTablePartitions(db, table, now, moveLimit)
		created += n
		if err != nil {
			errs = append(errs, fmt.Errorf("maintaining partitions of %s: %w", table.name, err))
		}
	}
	return created, errors.Join(errs...)
}

func errCatchAllBacklog(table partitionedTable, held int64) error {
	return fmt.Errorf("the catch-all partition holds %d rows to move (periodic limit %d); moving them blocks writes to %s, so run the migration command (-migrate up) in a low-traffic window", held, maxCatchAllMove, table.name)
}

func maintainTablePartitions(db *gorm.DB, table partitionedTable, now time.Time, moveLimit int64) (int, error) {
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
		// REORGANIZE rewrites every row of the catch-all; while it is empty
		// (the normal case, since partitions are created ahead) this only
		// creates empty partitions.
		// REORGANIZE 会重写兜底分区中的每一行；兜底分区为空时（正常情况，分区总是提前建好）
		// 只会建出空分区。
		var held int64
		if err := db.Raw("SELECT COUNT(*) FROM " + table.name + " PARTITION (" + catchAll + ")").Scan(&held).Error; err != nil {
			return 0, fmt.Errorf("counting the catch-all partition failed: %w", err)
		}
		if moveLimit > 0 && held > moveLimit {
			return 0, errCatchAllBacklog(table, held)
		}
		if held > 0 {
			slog.Warn("moving rows out of a catch-all partition", slog.String("table", table.name), slog.Int64("rows", held))
		}
		defs := append(mysqlRangeDefs(table, missing), "PARTITION "+catchAll+" VALUES LESS THAN (MAXVALUE)")
		err = withMySQLLockWait(db, func() error {
			return db.Exec("ALTER TABLE " + table.name + " REORGANIZE PARTITION " + catchAll + " INTO (" + strings.Join(defs, ", ") + ")").Error
		})
		if err != nil {
			return 0, fmt.Errorf("reorganizing the catch-all partition failed: %w", err)
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
		var held int64
		if err := tx.Raw("SELECT count(*) FROM "+def+" WHERE "+table.column+" >= ? AND "+table.column+" < ?", from, to).Scan(&held).Error; err != nil {
			return fmt.Errorf("inspecting the default partition failed: %w", err)
		}
		if moveLimit > 0 && held > moveLimit {
			return errCatchAllBacklog(table, held)
		}
		// A new partition cannot be created while the default partition holds
		// rows in its range, so those rows are moved through a detached default.
		// 默认分区中仍有落在新分区范围内的行时无法建分区，因此先摘下默认分区再搬移这些行。
		if held > 0 {
			slog.Warn("moving rows out of a default partition", slog.String("table", table.name), slog.Int64("rows", held))
			if err := tx.Exec("ALTER TABLE " + table.name + " DETACH PARTITION " + def).Error; err != nil {
				return fmt.Errorf("detaching the default partition failed: %w", err)
			}
		}
		for _, r := range missing {
			if err := tx.Exec(postgresCreatePartition(table, r)).Error; err != nil {
				return fmt.Errorf("creating a partition failed: %w", err)
			}
		}
		if held > 0 {
			move := "WITH moved AS (DELETE FROM " + def + " WHERE " + table.column + " >= ? AND " + table.column + " < ? RETURNING *) INSERT INTO " + table.name + " SELECT * FROM moved"
			if err := tx.Exec(move, from, to).Error; err != nil {
				return fmt.Errorf("moving rows out of the default partition failed: %w", err)
			}
			if err := tx.Exec("ALTER TABLE " + table.name + " ATTACH PARTITION " + def + " DEFAULT").Error; err != nil {
				return fmt.Errorf("re-attaching the default partition failed: %w", err)
			}
		}
		created = len(missing)
		return nil
	})
	return created, err
}

// dropExpiredPartitions drops every regular partition of table lying wholly
// before cutoff. It waits up to retentionLockPatience for the partition lock
// and returns errMigrationLockBusy if it never gets it.
//
// dropExpiredPartitions 删除 table 中整个区间都早于 cutoff 的常规分区。它最多等待
// retentionLockPatience 获取分区锁，始终拿不到时返回 errMigrationLockBusy。
func (s *Store) dropExpiredPartitions(ctx context.Context, table partitionedTable, cutoff time.Time) (int64, error) {
	var dropped int64
	err := s.withSessionLockPatiently(ctx, s.ddlDB(), retentionLockPatience, func(db *gorm.DB) error {
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
				return fmt.Errorf("dropping expired partitions of %s failed: %w", table.name, err)
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
				return fmt.Errorf("dropping expired partitions of %s failed: %w", table.name, err)
			}
			dropped = int64(len(names))
			return nil
		})
	})
	return dropped, err
}

// applyRetention enforces "kept at least until cutoff": the cutoff is floored
// to its partition's start, every partition wholly before that is dropped,
// and the partition straddling the cutoff is kept whole, so a steady-state
// pass deletes no rows one by one. Rows below the floor that sit outside a
// dropped partition (late writes in the PostgreSQL default partition, or in
// MySQL's lowest partition, which has no lower bound) are removed by batched
// deletes.
//
// When the lock stays busy, another session is doing partition DDL and the
// pass is skipped. A metadata-lock timeout fails the pass for the next one to
// retry. Any other DDL failure — typically a runtime account without DDL
// rights — falls back to batched row deletes, so retention never stops.
//
// applyRetention 落实「至少保留到 cutoff」：把 cutoff 向下取整到所在分区的起点，删除
// 整个区间都早于该点的分区，跨越 cutoff 的分区整段保留，因此稳态下不会逐行删除。低于
// 取整点、又不在被删分区里的行（PostgreSQL 默认分区里的迟到写入，或 MySQL 没有下界的
// 最低分区里的行）用分批删除清掉。
//
// 锁一直被占用时，说明另一个会话正在执行分区 DDL，本轮跳过。元数据锁等待超时使本轮
// 失败，由下一轮重试。其它 DDL 失败（通常是运行账号没有 DDL 权限）退回分批行删除，保证
// 保留期不会停摆。
func (s *Store) applyRetention(ctx context.Context, tableName string, cutoff time.Time) (store.RetentionResult, error) {
	table, ok := lookupPartitionedTable(tableName)
	if !ok {
		return store.RetentionResult{}, fmt.Errorf("%s is not a partitioned table", tableName)
	}
	floor := table.unit.start(cutoff)
	dropped, err := s.dropExpiredPartitions(ctx, table, floor)
	switch {
	case errors.Is(err, errMigrationLockBusy):
		return store.RetentionResult{}, nil
	case err != nil && ctx.Err() != nil:
		return store.RetentionResult{}, err
	case isLockWaitTimeout(err):
		return store.RetentionResult{}, fmt.Errorf("dropping expired partitions of %s timed out waiting for a metadata lock; the next pass retries: %w", table.name, err)
	case err != nil:
		rows, delErr := deleteInBatches(ctx, s.db, table, floor)
		return store.RetentionResult{RowsDeleted: rows}, errors.Join(
			fmt.Errorf("partition DDL on %s failed, so expired rows were deleted row by row instead; grant the maintenance account DDL rights or set Database.MaintenanceDSN: %w", table.name, err), delErr)
	}
	rows, err := deleteInBatches(ctx, s.db, table, floor)
	return store.RetentionResult{PartitionsDropped: dropped, RowsDeleted: rows}, err
}

// deleteInBatches deletes the rows of table before cutoff, deleteBatchSize at
// a time, each batch its own short transaction.
//
// deleteInBatches 删除 table 中早于 cutoff 的行，每批 deleteBatchSize 行，每批各自是
// 一个短事务。
func deleteInBatches(ctx context.Context, db *gorm.DB, table partitionedTable, cutoff time.Time) (int64, error) {
	statement := fmt.Sprintf("DELETE FROM %s WHERE %s < ? LIMIT %d", table.name, table.column, deleteBatchSize)
	if db.Name() == "postgres" {
		// PostgreSQL has no DELETE ... LIMIT; the primary key picks the batch.
		// PostgreSQL 没有 DELETE ... LIMIT，用主键圈定每一批。
		statement = fmt.Sprintf("DELETE FROM %[1]s WHERE (id, %[2]s) IN (SELECT id, %[2]s FROM %[1]s WHERE %[2]s < ? LIMIT %[3]d)", table.name, table.column, deleteBatchSize)
	}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		res := db.WithContext(ctx).Exec(statement, cutoff)
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
		if res.RowsAffected < int64(deleteBatchSize) {
			return total, nil
		}
	}
}

// withSessionLockPatiently retries the non-blocking migration lock until it is
// acquired, patience runs out or ctx ends.
//
// withSessionLockPatiently 反复尝试以不等待方式获取迁移锁，直到成功、耐心耗尽或 ctx 结束。
func (s *Store) withSessionLockPatiently(ctx context.Context, db *gorm.DB, patience time.Duration, run func(*gorm.DB) error) error {
	deadline := time.Now().Add(patience)
	for {
		err := s.withSessionLockOn(ctx, db, run)
		if !errors.Is(err, errMigrationLockBusy) || !time.Now().Before(deadline) {
			return err
		}
		timer := time.NewTimer(lockPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// isLockWaitTimeout reports a DDL that gave up waiting for a metadata lock
// (MySQL 1205, PostgreSQL 55P03 lock_not_available).
//
// isLockWaitTimeout 判断 DDL 是否因等待元数据锁超时而放弃（MySQL 1205，PostgreSQL
// 55P03 lock_not_available）。
func isLockWaitTimeout(err error) bool {
	if err == nil {
		return false
	}
	var mysqlErr *mysqldriver.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1205
	}
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "55P03"
	}
	return false
}

// withMySQLLockWait bounds metadata-lock waits for partition DDL on the pinned
// session, then restores the session's previous value. The connection returns
// to the pool afterwards, so if the restore fails the connection is discarded
// rather than handed on with the short timeout.
//
// withMySQLLockWait 在固定的会话上限制分区 DDL 等待元数据锁的时长，之后恢复该会话原来
// 的值。这个连接随后会回到连接池，因此恢复失败时丢弃该连接，而不是带着短超时交给别人。
func withMySQLLockWait(db *gorm.DB, run func() error) error {
	var previous int64
	if err := db.Raw("SELECT @@SESSION.lock_wait_timeout").Scan(&previous).Error; err != nil {
		return err
	}
	if err := db.Exec(fmt.Sprintf("SET SESSION lock_wait_timeout = %d", int(partitionLockWait.Seconds()))).Error; err != nil {
		return err
	}
	defer func() {
		restore, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := db.WithContext(restore).Exec(fmt.Sprintf("SET SESSION lock_wait_timeout = %d", previous)).Error; err != nil {
			discardConnection(db)
		}
	}()
	return run()
}

// discardConnection marks the pinned connection bad so the pool closes it.
//
// discardConnection 把固定的连接标记为失效，让连接池关闭它。
func discardConnection(db *gorm.DB) {
	if conn, ok := db.Statement.ConnPool.(*sql.Conn); ok {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
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
