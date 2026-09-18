package gormstore

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// -----------------------------------------------------------------------
// UsageRecords
// -----------------------------------------------------------------------

// CreateUsageRecords implements store.UsageRecords. It writes the whole
// batch in one statement and relies on the id primary key's unique
// constraint plus clause.OnConflict{DoNothing: true} for idempotency,
// rather than reading existing rows first — mirroring CreateRequestLogs
// exactly, for the same reason: a retried push must stay a single round
// trip, not a read-then-write.
//
// CreateUsageRecords 实现 store.UsageRecords。它一条语句写入整批数据，
// 幂等性靠 id 主键的唯一约束加上 clause.OnConflict{DoNothing: true} 保证，
// 而不是先读已有行——与 CreateRequestLogs 完全对应，理由相同：一次被重试的
// 推送必须保持单次往返，而不是先读后写。
func (s *Store) CreateUsageRecords(ctx context.Context, records []model.UsageRecord) error {
	if len(records) == 0 {
		return nil
	}
	return translate(s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&records).Error)
}

// usageSummaryRow is SummarizeUsage's raw scan target — GORM's Scan into a
// slice of anonymous structs needs a named type with matching column-name
// tags.
//
// usageSummaryRow 是 SummarizeUsage 的原始扫描目标——GORM 把结果 Scan 进一个
// 匿名结构体切片时，需要一个带匹配列名 tag 的具名类型。
type usageSummaryRow struct {
	TenantID         string `gorm:"column:tenant_id"`
	Model            string `gorm:"column:model"`
	PromptTokens     int64  `gorm:"column:prompt_tokens"`
	CompletionTokens int64  `gorm:"column:completion_tokens"`
	TotalTokens      int64  `gorm:"column:total_tokens"`
	RequestCount     int64  `gorm:"column:request_count"`
}

// SummarizeUsage implements store.UsageRecords — the settlement query. It
// aggregates in the database rather than reading every row and summing in
// Go, so a wide window over a busy tenant does not have to pull its full
// row set across the wire just to add up three columns.
//
// SummarizeUsage 实现 store.UsageRecords——即结算查询。它在数据库内聚合，
// 而不是读出每一行再在 Go 里求和，这样一个繁忙租户的宽时间窗口查询，不必
// 只为了把三列相加就把整批行拉过网络。
func (s *Store) SummarizeUsage(ctx context.Context, filter store.UsageRecordFilter) ([]store.UsageSummary, error) {
	db := s.db.WithContext(ctx).Model(&model.UsageRecord{})
	if filter.TenantID != "" {
		db = db.Where("tenant_id = ?", filter.TenantID)
	}
	if !filter.Since.IsZero() {
		db = db.Where("created_at >= ?", filter.Since)
	}
	if !filter.Until.IsZero() {
		db = db.Where("created_at < ?", filter.Until)
	}
	var rows []usageSummaryRow
	err := db.Select(
		"tenant_id, model, " +
			"SUM(prompt_tokens) AS prompt_tokens, " +
			"SUM(completion_tokens) AS completion_tokens, " +
			"SUM(total_tokens) AS total_tokens, " +
			"COUNT(*) AS request_count",
	).Group("tenant_id, model").Order("tenant_id, model").Scan(&rows).Error
	if err != nil {
		return nil, translate(err)
	}
	out := make([]store.UsageSummary, len(rows))
	for i, r := range rows {
		out[i] = store.UsageSummary{
			TenantID: r.TenantID, Model: r.Model,
			PromptTokens: r.PromptTokens, CompletionTokens: r.CompletionTokens,
			TotalTokens: r.TotalTokens, RequestCount: r.RequestCount,
		}
	}
	return out, nil
}

// DeleteUsageRecordsBefore implements store.UsageRecords.
//
// DeleteUsageRecordsBefore 实现 store.UsageRecords。
func (s *Store) DeleteUsageRecordsBefore(ctx context.Context, before time.Time) (int64, error) {
	res := s.db.WithContext(ctx).Where("created_at < ?", before).Delete(&model.UsageRecord{})
	return res.RowsAffected, translate(res.Error)
}
