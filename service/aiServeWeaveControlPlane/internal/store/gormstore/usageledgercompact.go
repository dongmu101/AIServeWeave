package gormstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// compactScanBatch bounds how many ledger entries CompactUsageLedger reads at
// a time, and compactDeleteBatch how many ids one DELETE names.
//
// compactScanBatch 限制 CompactUsageLedger 每次读取的账目条数，compactDeleteBatch 限制
// 一条 DELETE 列出的 id 数。
const (
	compactScanBatch   = 5000
	compactDeleteBatch = 1000
)

// CompactUsageLedger merges each run of consecutive usage entries of a
// tenant that fall on the same day in loc, with no other entry between
// them, into the run's first entry: amounts added up, the last
// balance_after, the run's period and request count. Before
// .scratch/token-pricing every pushed batch wrote its own usage entry; this
// is the optional clean-up of those, and the only code that rewrites ledger
// entries. The balance chain is the same before and after. Each run is
// merged in one transaction under the tenant's billing-row lock; a merged
// entry is merged again with what follows it, so the command can be
// interrupted and run again. progress is called after each tenant.
//
// CompactUsageLedger 把租户在 loc 中同一天、中间没有其他账目的每一串连续用量账目，合并
// 进这一串的第一条：金额相加、取最后一条的 balance_after、记下这一串的时段与请求数。
// .scratch/token-pricing 之前，每个推送批次都会写一条用量账目；这是对它们的可选清理，
// 也是唯一会改写账目的代码。合并前后余额链完全相同。每一串在租户计费行锁下的一个事务中
// 合并；已合并的账目会与其后的账目再次合并，因此命令可以中断后再次执行。每处理完一个租户
// 调用一次 progress。
func (s *Store) CompactUsageLedger(ctx context.Context, loc *time.Location, progress func(tenants int, merged int64)) (int64, error) {
	if loc == nil {
		loc = time.UTC
	}
	var tenants []string
	if err := s.db.WithContext(ctx).Model(&model.CreditLedgerEntry{}).
		Where("kind = ?", model.LedgerKindUsage).Distinct("tenant_id").Order("tenant_id").Pluck("tenant_id", &tenants).Error; err != nil {
		return 0, translate(err)
	}
	var merged int64
	for i, tenantID := range tenants {
		n, err := s.compactTenantLedger(ctx, tenantID, loc)
		merged += n
		if err != nil {
			return merged, err
		}
		if progress != nil {
			progress(i+1, merged)
		}
	}
	return merged, nil
}

// compactTenantLedger compacts one tenant's ledger, reading it oldest first
// in batches and merging every run as soon as it ends.
//
// compactTenantLedger 压缩一个租户的账目：从旧到新分批读取，每一串一结束就合并。
func (s *Store) compactTenantLedger(ctx context.Context, tenantID string, loc *time.Location) (int64, error) {
	var (
		run     []model.CreditLedgerEntry
		merged  int64
		afterAt time.Time
		afterID string
		started bool
	)
	flush := func() error {
		if len(run) > 1 {
			if err := s.mergeLedgerRun(ctx, tenantID, run); err != nil {
				return err
			}
			merged += int64(len(run) - 1)
		}
		run = run[:0]
		return nil
	}
	for {
		query := s.db.WithContext(ctx).Where("tenant_id = ?", tenantID)
		if started {
			query = query.Where("(created_at > ? OR (created_at = ? AND id > ?))", afterAt, afterAt, afterID)
		}
		var batch []model.CreditLedgerEntry
		if err := query.Order("created_at, id").Limit(compactScanBatch).Find(&batch).Error; err != nil {
			return merged, translate(err)
		}
		for _, e := range batch {
			sameRun := e.Kind == model.LedgerKindUsage && len(run) > 0 && sameLocalDay(run[0].CreatedAt, e.CreatedAt, loc)
			if !sameRun {
				if err := flush(); err != nil {
					return merged, err
				}
			}
			if e.Kind == model.LedgerKindUsage {
				run = append(run, e)
			}
		}
		if len(batch) < compactScanBatch {
			return merged, flush()
		}
		last := batch[len(batch)-1]
		afterAt, afterID, started = last.CreatedAt, last.ID, true
	}
}

// mergeLedgerRun merges run (oldest first, at least two entries) into its
// first entry and deletes the rest.
//
// mergeLedgerRun 把 run（从旧到新，至少两条）合并进第一条，并删除其余各条。
func (s *Store) mergeLedgerRun(ctx context.Context, tenantID string, run []model.CreditLedgerEntry) error {
	first, last := run[0], run[len(run)-1]
	amount, count := int64(0), int64(0)
	for _, e := range run {
		amount += e.AmountMicro
		count += ledgerRequestCount(e)
	}
	start, end := first.CreatedAt, last.CreatedAt
	if first.PeriodStart != nil {
		start = *first.PeriodStart
	}
	if last.PeriodEnd != nil {
		end = *last.PeriodEnd
	}
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row model.TenantBilling
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ?", tenantID).Take(&row).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Model(&model.CreditLedgerEntry{}).Where("id = ?", first.ID).Updates(map[string]any{
			"amount_micro": amount, "balance_after_micro": last.BalanceAfterMicro,
			"ref": fmt.Sprintf("%d requests", count), "request_count": count,
			"period_start": start, "period_end": end,
		}).Error; err != nil {
			return err
		}
		ids := make([]string, 0, len(run)-1)
		for _, e := range run[1:] {
			ids = append(ids, e.ID)
		}
		for len(ids) > 0 {
			n := min(len(ids), compactDeleteBatch)
			if err := tx.Where("id IN ?", ids[:n]).Delete(&model.CreditLedgerEntry{}).Error; err != nil {
				return err
			}
			ids = ids[n:]
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted}))
}

// ledgerRequestCount is how many requests a usage entry covers: its
// request_count, or the count in a pre-upgrade "N requests" ref.
//
// ledgerRequestCount 是一条用量账目涵盖的请求数：它的 request_count，或者升级前
// "N requests" 形式的 ref 中的数字。
func ledgerRequestCount(e model.CreditLedgerEntry) int64 {
	if e.RequestCount > 0 {
		return e.RequestCount
	}
	var n int64
	if _, err := fmt.Sscanf(e.Ref, "%d requests", &n); err != nil || n < 0 {
		return 0
	}
	return n
}

// sameLocalDay reports whether a and b fall on the same calendar day in loc.
//
// sameLocalDay 报告 a 与 b 在 loc 中是否落在同一个日历日。
func sameLocalDay(a, b time.Time, loc *time.Location) bool {
	ay, am, ad := a.In(loc).Date()
	by, bm, bd := b.In(loc).Date()
	return ay == by && am == bm && ad == bd
}
