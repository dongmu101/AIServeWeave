// Package auditretention periodically deletes audit_logs rows past their
// retention window (Config.AuditLogRetention, 400 days by default). It is
// wired to store.AuditRetention, kept apart from store.Audit so the interface
// logic and handlers take still has no delete. It copies usageretention's
// shape rather than abstracting over it, the same choice that package's own
// doc comment explains.
//
// auditretention 包定时删除超出保留期（Config.AuditLogRetention，默认 400 天）的
// audit_logs 行。它装配的是 store.AuditRetention，与 store.Audit 分开，使 logic 与
// handler 取用的接口依然没有删除方法。它复制 usageretention 的形状而不是为之抽象，
// 理由与那个包自己的文档注释所述相同。
package auditretention

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
	DeleteAuditLogsBefore(ctx context.Context, before time.Time) (store.RetentionResult, error)
}

// Retention periodically deletes audit_logs rows past their retention
// window.
//
// Retention 定时删除超出保留期的 audit_logs 行。
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
	result, err := r.store.DeleteAuditLogsBefore(ctx, before)
	if err != nil {
		return err
	}
	if result.PartitionsDropped > 0 || result.RowsDeleted > 0 {
		r.logger.Info("audit log retention cleanup", slog.String("table", "audit_logs"), slog.Time("before", before),
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
				r.logger.Error("audit log retention cleanup failed", slog.Any("error", err))
			}
		}
	}
}
