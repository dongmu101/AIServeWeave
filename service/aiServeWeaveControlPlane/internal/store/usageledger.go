package store

import (
	"context"
	"fmt"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// Usage ledger rules (.scratch/token-pricing/spec.md「余额账目」). Charging
// takes each batch off the balance at once but does not write a ledger
// entry: it accumulates the amount on the tenant's billing row, and a usage
// entry is written only when the accumulation is sealed — when its day ends,
// right before any other ledger entry for the tenant, or by the periodic
// seal. Ledger entries are therefore only ever inserted, never updated, and
// the balance chain holds: each entry's balance_after is the previous one's
// plus its amount, and the latest entry's balance_after less the pending
// accumulation is the balance. Both stores call these functions with the
// tenant's billing row locked.
//
// 用量账目规则（.scratch/token-pricing/spec.md「余额账目」）。扣费立即从余额中扣掉每
// 一批，但不写账目：金额累计在租户的计费行上，只有在累计被封账时才写一条用量账目——当天
// 结束时、该租户写入任何其他账目之前，或由定时封账任务。因此账目只插入、从不更新，余额链
// 也成立：每条账目的 balance_after 等于上一条加上本条金额，最新一条的 balance_after 减去
// 尚未封账的累计等于余额。两套 store 都在锁住租户计费行时调用这些函数。

// SealDueBatch bounds how many tenants one SealDueUsage pass seals.
//
// SealDueBatch 限制一轮 SealDueUsage 封账的租户数。
const SealDueBatch = 500

// UsageLedger is the store surface of the usage ledger rules.
//
// UsageLedger 是用量账目规则在 store 上的接口。
type UsageLedger interface {
	// SealDueUsage seals, each in its own transaction, the accumulations
	// whose deadline is at or before now, at most SealDueBatch of them, and
	// returns how many it sealed.
	//
	// SealDueUsage 逐个在各自的事务中，为封账期限不晚于 now 的累计封账，最多
	// SealDueBatch 个，返回封账的个数。
	SealDueUsage(ctx context.Context, now time.Time) (int, error)
}

// NextLedgerTime returns the created_at a new ledger entry of a tenant gets:
// now at microsecond precision (what both databases keep), but strictly
// after the tenant's latest entry, so a sealed usage entry and the top-up
// that sealed it never share a timestamp and replicas' clock skew cannot
// order entries backwards.
//
// NextLedgerTime 返回租户一条新账目的 created_at：精确到微秒的 now（两种数据库保存的
// 精度），但严格晚于该租户最新的一条账目，使封账的用量账目与触发封账的充值账目不会同一
// 时刻，各副本之间的时钟误差也不会让账目倒序。
func NextLedgerTime(last *time.Time, now time.Time) time.Time {
	at := now.UTC().Truncate(time.Microsecond)
	if last != nil && !at.After(*last) {
		at = last.UTC().Truncate(time.Microsecond).Add(time.Microsecond)
	}
	return at
}

// AccrueUsage adds a charged batch to the tenant's accumulation: amount
// micro-credits over count requests charged at now. A new accumulation is
// due for sealing at sealAt. The caller has already taken amount off the
// balance.
//
// AccrueUsage 把一个已扣费的批次加入租户的累计：在 now 扣费、涵盖 count 个请求、金额
// amount micro 点。新开的累计在 sealAt 到期封账。调用方已经从余额中扣掉 amount。
func AccrueUsage(row *model.TenantBilling, amount int64, count int, now, sealAt time.Time) {
	now = now.UTC()
	if row.UsageAccCount == 0 {
		since, until := now, sealAt.UTC()
		row.UsageAccSince, row.UsageAccUntil = &since, &until
	}
	row.UsageAccMicro += amount
	row.UsageAccCount += int64(count)
	row.UsageAccLast = &now
}

// SealDue reports whether the tenant's accumulation must be sealed at now.
//
// SealDue 报告租户的累计在 now 时是否必须封账。
func SealDue(row model.TenantBilling, now time.Time) bool {
	return row.UsageAccCount > 0 && row.UsageAccUntil != nil && !now.Before(*row.UsageAccUntil)
}

// SealUsage turns the tenant's accumulation into a usage ledger entry dated
// by NextLedgerTime, clears the accumulation and advances LastLedgerAt. It
// returns nil when nothing is accumulating. The entry's balance_after is the
// current balance, which already reflects every accumulated charge.
//
// SealUsage 把租户的累计变成一条按 NextLedgerTime 定时的用量账目，清空累计并推进
// LastLedgerAt。没有累计时返回 nil。账目的 balance_after 就是当前余额，它已经反映了
// 全部累计的扣费。
func SealUsage(row *model.TenantBilling, now time.Time) *model.CreditLedgerEntry {
	if row.UsageAccCount == 0 {
		return nil
	}
	at := NextLedgerTime(row.LastLedgerAt, now)
	entry := &model.CreditLedgerEntry{
		ID: model.NewID(model.PrefixCreditLedger), TenantID: row.TenantID, Kind: model.LedgerKindUsage,
		AmountMicro: -row.UsageAccMicro, BalanceAfterMicro: row.BalanceMicro,
		Ref:         fmt.Sprintf("%d requests", row.UsageAccCount),
		PeriodStart: row.UsageAccSince, PeriodEnd: row.UsageAccLast, RequestCount: row.UsageAccCount,
		CreatedAt: at,
	}
	ClearUsageAccumulation(row)
	row.LastLedgerAt = &at
	return entry
}

// ClearUsageAccumulation resets the accumulation fields.
//
// ClearUsageAccumulation 重置累计字段。
func ClearUsageAccumulation(row *model.TenantBilling) {
	row.UsageAccMicro, row.UsageAccCount = 0, 0
	row.UsageAccSince, row.UsageAccLast, row.UsageAccUntil = nil, nil, nil
}

// UsageAccumulationColumns returns the billing-row columns the usage ledger
// rules change, for a gorm column update.
//
// UsageAccumulationColumns 返回用量账目规则会改动的计费行列，供 gorm 按列更新。
func UsageAccumulationColumns(row model.TenantBilling) map[string]any {
	return map[string]any{
		"usage_acc_micro": row.UsageAccMicro, "usage_acc_count": row.UsageAccCount,
		"usage_acc_since": row.UsageAccSince, "usage_acc_last": row.UsageAccLast, "usage_acc_until": row.UsageAccUntil,
		"last_ledger_at": row.LastLedgerAt,
	}
}

// NextLocalMidnight returns the first midnight in loc after now: the end of
// now's day on the ledger's clock.
//
// NextLocalMidnight 返回 loc 中晚于 now 的第一个午夜：按账目时钟计，now 所在那一天的
// 结束时刻。
func NextLocalMidnight(now time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, loc).UTC()
}
