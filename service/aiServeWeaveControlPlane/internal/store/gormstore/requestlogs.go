package gormstore

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// -----------------------------------------------------------------------
// RequestLogs
// -----------------------------------------------------------------------

// CreateRequestLogs implements store.RequestLogs. It writes the whole batch
// in one statement and relies on the id primary key's unique constraint plus
// clause.OnConflict{DoNothing: true} for idempotency, rather than reading
// existing rows first — a retried push is expected to be common (STATUS.md's
// P09/C28 Gateway-side outbox), so this stays a single round trip instead of
// a read-then-write.
//
// CreateRequestLogs 实现 store.RequestLogs。它一条语句写入整批数据，幂等性靠 id
// 主键的唯一约束加上 clause.OnConflict{DoNothing: true} 保证，而不是先读已有行——
// 重复推送预期是常态（对应 STATUS.md 的 P09/C28 Gateway 侧 outbox），所以这里保持
// 单次往返，而不是先读后写。
func (s *Store) CreateRequestLogs(ctx context.Context, records []model.RequestLog) error {
	if len(records) == 0 {
		return nil
	}
	return translate(s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&records).Error)
}

// ListRequestLogs implements store.RequestLogs.
//
// ListRequestLogs 实现 store.RequestLogs。
func (s *Store) ListRequestLogs(ctx context.Context, query store.ListQuery, filter store.RequestLogFilter) (store.Page[model.RequestLog], error) {
	db := s.db.WithContext(ctx).Model(&model.RequestLog{})
	if filter.TenantID != "" {
		db = db.Where("tenant_id = ?", filter.TenantID)
	}
	if filter.RequestID != "" {
		db = db.Where("id = ?", filter.RequestID)
	}
	if filter.Outcome != "" {
		db = db.Where("outcome = ?", filter.Outcome)
	}
	if !filter.Since.IsZero() {
		db = db.Where("created_at >= ?", filter.Since)
	}
	if !filter.Until.IsZero() {
		db = db.Where("created_at < ?", filter.Until)
	}
	return readPage(db, query, func(r model.RequestLog) (time.Time, string) { return r.CreatedAt, r.ID })
}

// DeleteRequestLogsBefore implements store.RequestLogs.
//
// DeleteRequestLogsBefore 实现 store.RequestLogs。
func (s *Store) DeleteRequestLogsBefore(ctx context.Context, before time.Time) (int64, error) {
	res := s.db.WithContext(ctx).Where("created_at < ?", before).Delete(&model.RequestLog{})
	return res.RowsAffected, translate(res.Error)
}
