package metricshistory

import (
	"context"
	"log/slog"
	"time"

	"AIServeWeave/common/runtime"
)

// RetentionStore is the persistence surface retention cleanup needs.
//
// RetentionStore 是保留期清理所需要的持久化接口。
type RetentionStore interface {
	DeleteRollupBefore(ctx context.Context, before time.Time) (int64, error)
}

// Retention periodically deletes rollup rows past their retention window.
//
// Retention 定时删除超出保留期的汇总行。
type Retention struct {
	store     RetentionStore
	retention time.Duration
	clock     runtime.Clock
	logger    *slog.Logger
}

// NewRetention builds a Retention. A nil clock defaults to the system clock;
// a nil logger discards.
//
// NewRetention 构造一个 Retention。clock 为 nil 时使用系统时钟；logger 为 nil
// 时丢弃日志。
func NewRetention(store RetentionStore, retention time.Duration, clock runtime.Clock, logger *slog.Logger) *Retention {
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
	n, err := r.store.DeleteRollupBefore(ctx, before)
	if err != nil {
		return err
	}
	if n > 0 {
		r.logger.Info("metrics history retention cleanup", slog.Int64("rows_deleted", n), slog.Time("before", before))
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
				r.logger.Error("metrics history retention cleanup failed", slog.Any("error", err))
			}
		}
	}
}
