// Package partitionmaint keeps the time-partitioned tables' future partitions
// created ahead of the clock: hourly, and once right after startup. Each
// table also has a catch-all partition, so a late or failed pass never fails a
// write; it only lets rows collect in the catch-all until the next pass moves
// them. Like the retention sweepers, it is a timer around one store call; the
// dialect-specific DDL lives in gormstore.
//
// partitionmaint 包让按时间分区的表始终预先建好未来的分区：每小时一次，启动后立即
// 一次。每张表还有一个兜底分区，因此维护迟到或失败都不会让写入失败，只会让行暂时积在
// 兜底分区里，直到下一轮把它们搬走。它与保留期清理协程一样，只是包着一次 store 调用的
// 定时器；具体方言的 DDL 在 gormstore 中。
package partitionmaint

import (
	"context"
	"log/slog"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// Interval is how often maintenance runs. Daily tables keep seven days ahead,
// so an hourly pass leaves a wide margin.
//
// Interval 是维护的运行频率。按天分区的表预建 7 天，每小时一轮留有充足余量。
const Interval = time.Hour

// StartupTimeout bounds the pass run synchronously at startup.
//
// StartupTimeout 限制启动时同步执行的那一轮。
const StartupTimeout = 30 * time.Second

// Store is the persistence surface maintenance needs.
//
// Store 是分区维护所需要的持久化接口。
type Store interface {
	MaintainPartitions(ctx context.Context, now time.Time) (store.PartitionMaintenanceResult, error)
}

// Maintainer runs partition maintenance.
//
// Maintainer 执行分区维护。
type Maintainer struct {
	store  Store
	clock  runtime.Clock
	logger *slog.Logger
}

// New builds a Maintainer. A nil clock defaults to the system clock; a nil
// logger discards.
//
// New 构造一个 Maintainer。clock 为 nil 时使用系统时钟；logger 为 nil 时丢弃日志。
func New(st Store, clock runtime.Clock, logger *slog.Logger) *Maintainer {
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Maintainer{store: st, clock: clock, logger: logger}
}

// RunOnce runs one maintenance pass. A pass skipped because another replica
// holds the lock is not an error.
//
// RunOnce 执行一轮维护。因其它副本持有锁而跳过不算错误。
func (m *Maintainer) RunOnce(ctx context.Context) error {
	result, err := m.store.MaintainPartitions(ctx, m.clock.Now())
	if err != nil {
		return err
	}
	switch {
	case result.Skipped:
		m.logger.Debug("partition maintenance skipped; another session holds the lock")
	case result.Created > 0:
		m.logger.Info("partition maintenance", slog.Int("partitions_created", result.Created))
	}
	return nil
}

// Run calls RunOnce once per interval until ctx is done, logging failures so
// the next pass retries.
//
// Run 每隔 interval 调用一次 RunOnce，直到 ctx 结束；失败只记日志，由下一轮重试。
func (m *Maintainer) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.RunOnce(ctx); err != nil {
				m.logger.Error("partition maintenance failed", slog.Any("error", err))
			}
		}
	}
}
