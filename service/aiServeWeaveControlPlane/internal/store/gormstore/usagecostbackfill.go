package gormstore

import (
	"context"
	"time"

	"gorm.io/gorm"

	"AIServeWeave/common/pricing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// UsageCostBackfillBatch is how many records BackfillUsageCost updates per
// transaction.
//
// UsageCostBackfillBatch 是 BackfillUsageCost 每个事务更新的记录数。
const UsageCostBackfillBatch = 1000

// BackfillUsageCost copies the cost of every record charged before pricing
// moved into usage_records (.scratch/token-pricing) from its usage_charges
// row: cost_micro and base_cost_micro, charged, rate_ppm 1x and
// price_source model.PriceSourceLegacy. Only records without a price source
// are touched, so it can run again or resume after an interruption; records
// with no charge row (written with billing off) stay as they are. It walks
// usage_records in (created_at, id) order along idx_usage_records_created,
// one batch per transaction, and reports progress after each batch.
//
// BackfillUsageCost 把计价移入 usage_records（.scratch/token-pricing）之前已扣费的
// 每条记录的费用，从其 usage_charges 行复制过来：cost_micro 与 base_cost_micro、
// charged、1 倍的 rate_ppm，以及价格来源 model.PriceSourceLegacy。只处理没有价格来源
// 的记录，因此可以重复执行，也可以在中断后继续；没有费用行的记录（计费关闭时写入）保持
// 原样。它沿 idx_usage_records_created 按 (created_at, id) 顺序遍历 usage_records，
// 每批一个事务，并在每批之后报告进度。
func (s *Store) BackfillUsageCost(ctx context.Context, progress func(updated int64)) (int64, error) {
	type row struct {
		ID        string
		CreatedAt time.Time
		CostMicro int64
		Charged   bool
	}
	var (
		updated     int64
		afterAt     time.Time
		afterID     string
		haveCursor  bool
		legacyRate  = int64(pricing.RateOne)
		priceSource = model.PriceSourceLegacy
	)
	for {
		query := s.db.WithContext(ctx).Table("usage_records AS r").
			Select("r.id, r.created_at, c.cost_micro, c.charged").
			Joins("JOIN usage_charges AS c ON c.request_id = r.id").
			Where("r.price_source = ?", "")
		if haveCursor {
			query = query.Where("(r.created_at > ? OR (r.created_at = ? AND r.id > ?))", afterAt, afterAt, afterID)
		}
		var rows []row
		if err := query.Order("r.created_at, r.id").Limit(UsageCostBackfillBatch).Scan(&rows).Error; err != nil {
			return updated, translate(err)
		}
		if len(rows) == 0 {
			return updated, nil
		}
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for _, r := range rows {
				res := tx.Model(&model.UsageRecord{}).
					Where("id = ? AND created_at = ? AND price_source = ?", r.ID, r.CreatedAt, "").
					Updates(map[string]any{
						"cost_micro": r.CostMicro, "base_cost_micro": r.CostMicro, "charged": r.Charged,
						"rate_ppm": legacyRate, "price_source": priceSource,
					})
				if res.Error != nil {
					return res.Error
				}
				updated += res.RowsAffected
			}
			return nil
		})
		if err != nil {
			return updated, translate(err)
		}
		last := rows[len(rows)-1]
		afterAt, afterID, haveCursor = last.CreatedAt, last.ID, true
		if progress != nil {
			progress(updated)
		}
	}
}
