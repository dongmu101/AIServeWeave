// Package usageretention periodically deletes usage_records rows past
// their retention window, per STATUS.md's P2 usage ledger. It is a
// separate, narrow package rather than a method tacked onto
// requestlogretention: the two tables are unrelated data with unrelated
// retention policies (a settlement ledger reasonably outlives a diagnostic
// request log), and the only thing they share is the shape of "delete rows
// older than a cutoff on a timer" — which this package copies rather than
// abstracts over, the same choice requestlogretention's own doc comment
// already explains for its relationship to metricshistory.
//
// usageretention 包定时删除超出保留期的 usage_records 行，对应 STATUS.md
// 的 P2 用量账本。它是一个独立的、窄的包，而不是挂在 requestlogretention
// 上的一个方法：这两张表是互不相关的数据、互不相关的保留策略（一份结算
// 账本合理地比一份诊断性请求日志活得更久），两者唯一的共同点只是"定时删除
// 早于某个截止时间的行"这个形状——本包选择复制这个形状而不是为它抽象出
// 共用逻辑，与 requestlogretention 自己的文档注释里对它与 metricshistory
// 关系的说明是同一个选择。
package usageretention

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
	DeleteUsageRecordsBefore(ctx context.Context, before time.Time) (int64, error)
}

// Retention periodically deletes usage_records rows past their retention
// window.
//
// Retention 定时删除超出保留期的 usage_records 行。
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
	n, err := r.store.DeleteUsageRecordsBefore(ctx, before)
	if err != nil {
		return err
	}
	if n > 0 {
		r.logger.Info("usage record retention cleanup", slog.Int64("rows_deleted", n), slog.Time("before", before))
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
				r.logger.Error("usage record retention cleanup failed", slog.Any("error", err))
			}
		}
	}
}
