// Package requestlogretention periodically deletes request_logs rows past
// their retention window, per STATUS.md's P09/C28. It is a separate,
// narrow package rather than a method tacked onto metricshistory: the two
// tables are unrelated data with unrelated retention policies, and the only
// thing they share is the shape of "delete rows older than a cutoff on a
// timer" — which this package copies rather than abstracts over, since a
// shared abstraction over two unrelated call sites would buy nothing but an
// extra layer of indirection.
//
// requestlogretention 包定时删除超出保留期的 request_logs 行，对应
// STATUS.md 的 P09/C28。它是一个独立的、窄的包，而不是挂在 metricshistory
// 上的一个方法：这两张表是互不相关的数据、互不相关的保留策略，两者唯一的
// 共同点只是"定时删除早于某个截止时间的行"这个形状——本包选择复制这个形状
// 而不是为它抽象出共用逻辑，因为给两个互不相关的调用点做一层共享抽象，除了
// 多一层间接之外什么都买不到。
package requestlogretention

import (
	"context"
	"log/slog"
	"time"

	"AIServeWeave/common/runtime"
)

// Store is the persistence surface retention cleanup needs.
//
// Store 是保留期清理所需要的持久化接口。
type Store interface {
	DeleteRequestLogsBefore(ctx context.Context, before time.Time) (int64, error)
}

// Retention periodically deletes request_logs rows past their retention
// window.
//
// Retention 定时删除超出保留期的 request_logs 行。
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
	n, err := r.store.DeleteRequestLogsBefore(ctx, before)
	if err != nil {
		return err
	}
	if n > 0 {
		r.logger.Info("request log retention cleanup", slog.Int64("rows_deleted", n), slog.Time("before", before))
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
				r.logger.Error("request log retention cleanup failed", slog.Any("error", err))
			}
		}
	}
}
