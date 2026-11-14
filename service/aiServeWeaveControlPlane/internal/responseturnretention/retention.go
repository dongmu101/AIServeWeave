// Package responseturnretention periodically deletes response_turns rows past
// their retention window (Config.ResponseTurnRetention, 30 days by default).
// A reaped turn reads back as unknown, so a previous_response_id naming it is
// answered exactly like an id that never existed. It copies usageretention's
// shape rather than abstracting over it, the same choice that package's own
// doc comment explains.
//
// responseturnretention 包定时删除超出保留期（Config.ResponseTurnRetention，默认
// 30 天）的 response_turns 行。被回收的轮次读取时等同未知，因此指向它的
// previous_response_id 与从未存在的 id 答复相同。它复制 usageretention 的形状而
// 不是为之抽象，理由与那个包自己的文档注释所述相同。
package responseturnretention

import (
	"context"
	"log/slog"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// Store is the persistence surface retention cleanup needs.
//
// Store 是保留期清理所需要的持久化接口。
type Store interface {
	DeleteResponseTurnsBefore(ctx context.Context, before time.Time) (store.RetentionResult, error)
}

// Retention periodically deletes response_turns rows past their retention
// window.
//
// Retention 定时删除超出保留期的 response_turns 行。
type Retention struct {
	store     Store
	retention time.Duration
	clock     runtime.Clock
	logger    *slog.Logger
}

// New builds a Retention. A nil clock defaults to the system clock; a nil
// logger discards.
//
// New 构造一个 Retention。clock 为 nil 时使用系统时钟；logger 为 nil 时
// 丢弃日志。
func New(store Store, retention time.Duration, clock runtime.Clock, logger *slog.Logger) *Retention {
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Retention{store: store, retention: retention, clock: clock, logger: logger}
}

// RunOnce deletes every row older than the retention window, once.
//
// RunOnce 一次性删除全部超出保留期的行。
func (r *Retention) RunOnce(ctx context.Context) error {
	before := r.clock.Now().Add(-r.retention)
	result, err := r.store.DeleteResponseTurnsBefore(ctx, before)
	if err != nil {
		return err
	}
	if result.PartitionsDropped > 0 || result.RowsDeleted > 0 {
		r.logger.Info("response turn retention cleanup", slog.String("table", "response_turns"), slog.Time("before", before),
			slog.Int64("partitions_dropped", result.PartitionsDropped), slog.Int64("rows_deleted", result.RowsDeleted))
	}
	return nil
}

// Run calls RunOnce once per interval until ctx is done.
//
// Run 每隔 interval 调用一次 RunOnce，直到 ctx 结束。
func (r *Retention) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.RunOnce(ctx); err != nil {
				r.logger.Error("response turn retention cleanup failed", slog.Any("error", err))
			}
		}
	}
}
