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
	TenantID         string    `gorm:"column:tenant_id"`
	UserID           string    `gorm:"column:user_id"`
	KeyID            string    `gorm:"column:key_id"`
	Model            string    `gorm:"column:model"`
	PromptTokens     int64     `gorm:"column:prompt_tokens"`
	CompletionTokens int64     `gorm:"column:completion_tokens"`
	TotalTokens      int64     `gorm:"column:total_tokens"`
	RequestCount     int64     `gorm:"column:request_count"`
	LastUsedAt       time.Time `gorm:"column:last_used_at"`
	CacheReadTokens  int64     `gorm:"column:cache_read_tokens"`
	CacheWriteTokens int64     `gorm:"column:cache_write_tokens"`
	CostMicro        int64     `gorm:"column:cost_micro"`
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
	groups := "tenant_id, model"
	lastUsed := ""
	if filter.ByKey {
		groups = "tenant_id, user_id, key_id, model"
		lastUsed = ", MAX(created_at) AS last_used_at"
	}
	var rows []usageSummaryRow
	err := db.Select(
		groups + ", " +
			"SUM(prompt_tokens) AS prompt_tokens, " +
			"SUM(completion_tokens) AS completion_tokens, " +
			"SUM(total_tokens) AS total_tokens, " +
			"COUNT(*) AS request_count, " +
			"SUM(cache_read_tokens) AS cache_read_tokens, " +
			"SUM(cache_write_tokens) AS cache_write_tokens, " +
			"SUM(cost_micro) AS cost_micro" + lastUsed,
	).Group(groups).Order(groups).Scan(&rows).Error
	if err != nil {
		return nil, translate(err)
	}
	out := make([]store.UsageSummary, len(rows))
	for i, r := range rows {
		out[i] = store.UsageSummary{
			TenantID: r.TenantID, UserID: r.UserID, KeyID: r.KeyID, Model: r.Model,
			PromptTokens: r.PromptTokens, CompletionTokens: r.CompletionTokens,
			TotalTokens: r.TotalTokens, RequestCount: r.RequestCount, LastUsedAt: r.LastUsedAt,
			CacheReadTokens: r.CacheReadTokens, CacheWriteTokens: r.CacheWriteTokens, CostMicro: r.CostMicro,
		}
	}
	return out, nil
}

// ListUsageRecords implements store.UsageRecords. A tenant's page walks
// idx_usage_records_tenant_created (tenant_id, created_at, id) backwards
// across the monthly partitions; the operator's cross-tenant page walks
// idx_usage_records_created.
//
// ListUsageRecords 实现 store.UsageRecords。租户的分页沿
// idx_usage_records_tenant_created (tenant_id, created_at, id) 跨月分区倒序扫描；运维
// 的跨租户分页沿 idx_usage_records_created 扫描。
func (s *Store) ListUsageRecords(ctx context.Context, query store.ListQuery, filter store.UsageRecordListFilter) (store.Page[model.UsageRecord], error) {
	db := s.db.WithContext(ctx).Model(&model.UsageRecord{})
	if filter.TenantID != "" {
		db = db.Where("tenant_id = ?", filter.TenantID)
	}
	if filter.Model != "" {
		db = db.Where("model = ?", filter.Model)
	}
	if filter.KeyID != "" {
		db = db.Where("key_id = ?", filter.KeyID)
	}
	if !filter.Since.IsZero() {
		db = db.Where("created_at >= ?", filter.Since)
	}
	if !filter.Until.IsZero() {
		db = db.Where("created_at < ?", filter.Until)
	}
	return readPage(db, query, func(r model.UsageRecord) (time.Time, string) { return r.CreatedAt, r.ID })
}

// CountUsageRecordsBySource implements store.UsageRecords. It scans the
// window along idx_usage_records_created, so it is meant for short windows.
//
// CountUsageRecordsBySource 实现 store.UsageRecords。它沿 idx_usage_records_created
// 扫描时间窗口，因此只适合短窗口。
func (s *Store) CountUsageRecordsBySource(ctx context.Context, source string, since time.Time) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.UsageRecord{}).
		Where("created_at >= ? AND price_source = ?", since, source).Count(&n).Error
	return n, translate(err)
}

// DeleteUsageRecordsBefore implements store.UsageRecords.
//
// DeleteUsageRecordsBefore 实现 store.UsageRecords。
func (s *Store) DeleteUsageRecordsBefore(ctx context.Context, before time.Time) (store.RetentionResult, error) {
	result, err := s.applyRetention(ctx, "usage_records", before)
	return result, translate(err)
}
