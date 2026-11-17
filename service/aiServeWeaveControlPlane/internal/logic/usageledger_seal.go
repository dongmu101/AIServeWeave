package logic

import (
	"context"
	"log/slog"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// UsageSealInterval is how often RunUsageLedgerSeal looks for accumulations
// past their deadline. A day's usage is sealed at most this long after the
// day ends when the tenant makes no further request.
//
// UsageSealInterval 是 RunUsageLedgerSeal 查找已过封账期限的累计的间隔。租户不再发出
// 请求时，一天的用量最迟在当天结束后这么久封账。
const UsageSealInterval = time.Minute

// RunUsageLedgerSeal seals overdue usage accumulations into ledger entries
// until ctx ends (.scratch/token-pricing/spec.md「余额账目」). Without it a
// tenant that stops using the service keeps its last day's usage unsealed.
// It does nothing when billing is off.
//
// RunUsageLedgerSeal 在 ctx 结束之前，把已过期限的用量累计封账成账目
// （.scratch/token-pricing/spec.md「余额账目」）。没有它，停止使用服务的租户最后一天的
// 用量会一直不封账。计费关闭时它什么都不做。
func (s *Service) RunUsageLedgerSeal(ctx context.Context) {
	if !s.billingEnabled() {
		return
	}
	for {
		fired, stop := s.clock.NewTimer(UsageSealInterval)
		select {
		case <-ctx.Done():
			stop()
			return
		case <-fired:
		}
		s.SealDueUsage(ctx)
	}
}

// SealDueUsage runs one sealing pass, continuing while full batches come
// back, and logs a failure rather than returning it: the next pass retries.
//
// SealDueUsage 执行一轮封账，取回的批次是满的就继续；失败只记日志而不返回，由下一轮
// 重试。
func (s *Service) SealDueUsage(ctx context.Context) {
	for ctx.Err() == nil {
		sealed, err := s.store.SealDueUsage(ctx, s.clock.Now())
		if err != nil {
			slog.Warn("sealing usage ledger entries failed", slog.Any("error", err))
			return
		}
		if sealed > 0 {
			slog.Info("sealed usage ledger entries", slog.Int("tenants", sealed))
		}
		if sealed < store.SealDueBatch {
			return
		}
	}
}
