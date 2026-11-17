package gormstore

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// revisionListColumns are the pricing_revisions columns a list reads: every
// column but the document, which can be megabytes.
//
// revisionListColumns 是版本列表读取的 pricing_revisions 列：除文档外的全部列，文档
// 可能有数 MB。
const revisionListColumns = "id, effective_at, created_by, summary, created_at"

// LatestPricingRevision implements store.PricingConfig.
//
// LatestPricingRevision 实现 store.PricingConfig。
func (s *Store) LatestPricingRevision(ctx context.Context) (model.PricingRevision, error) {
	return latestPricingRevision(s.db.WithContext(ctx))
}

func latestPricingRevision(db *gorm.DB) (model.PricingRevision, error) {
	var rev model.PricingRevision
	err := db.Order("id DESC").Take(&rev).Error
	return rev, translate(err)
}

// PricingRevisionAt implements store.PricingConfig.
//
// PricingRevisionAt 实现 store.PricingConfig。
func (s *Store) PricingRevisionAt(ctx context.Context, at time.Time) (model.PricingRevision, error) {
	db := s.db.WithContext(ctx)
	var rev model.PricingRevision
	err := db.Where("effective_at <= ?", at).Order("effective_at DESC, id DESC").Take(&rev).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = db.Order("effective_at ASC, id ASC").Take(&rev).Error
	}
	return rev, translate(err)
}

// GetPricingRevision implements store.PricingConfig.
//
// GetPricingRevision 实现 store.PricingConfig。
func (s *Store) GetPricingRevision(ctx context.Context, id int64) (model.PricingRevision, error) {
	var rev model.PricingRevision
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&rev).Error
	return rev, translate(err)
}

// ListPricingRevisions implements store.PricingConfig. Ids only grow, so the
// cursor carries the last id alone.
//
// ListPricingRevisions 实现 store.PricingConfig。id 只增不减，因此游标只携带最后一个 id。
func (s *Store) ListPricingRevisions(ctx context.Context, query store.ListQuery) (store.Page[model.PricingRevision], error) {
	after, err := decodeRevisionCursor(query.Cursor)
	if err != nil {
		return store.Page[model.PricingRevision]{}, err
	}
	db := s.db.WithContext(ctx).Model(&model.PricingRevision{}).Select(revisionListColumns)
	if after > 0 {
		db = db.Where("id < ?", after)
	}
	size := query.Size()
	var out []model.PricingRevision
	if err := db.Order("id DESC").Limit(size + 1).Find(&out).Error; err != nil {
		return store.Page[model.PricingRevision]{}, translate(err)
	}
	if len(out) <= size {
		return store.Page[model.PricingRevision]{Items: out}, nil
	}
	return store.Page[model.PricingRevision]{Items: out[:size], NextCursor: encodeRevisionCursor(out[size-1].ID)}, nil
}

// encodeRevisionCursor and decodeRevisionCursor reuse the store's cursor
// encoding with a zero time, so a cursor from another list is refused as
// malformed rather than misread.
//
// encodeRevisionCursor 与 decodeRevisionCursor 复用 store 的游标编码（时间为零值），
// 使来自其他列表的游标被当作畸形游标拒绝，而不是被误读。
func encodeRevisionCursor(id int64) string {
	return store.EncodeCursor(time.Time{}, strconv.FormatInt(id, 10))
}

func decodeRevisionCursor(cursor string) (int64, error) {
	at, raw, err := store.DecodeCursor(cursor)
	if err != nil || cursor == "" {
		return 0, err
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || !at.IsZero() {
		return 0, store.ErrInvalidCursor
	}
	return id, nil
}

// CreateInitialPricingRevision implements store.PricingConfig.
//
// CreateInitialPricingRevision 实现 store.PricingConfig。
func (s *Store) CreateInitialPricingRevision(ctx context.Context, rev model.PricingRevision) (model.PricingRevision, bool, error) {
	created := false
	err := s.pricingTx(ctx, rev.CreatedAt, func(tx *gorm.DB) error {
		latest, err := latestPricingRevision(tx)
		if err == nil {
			rev = latest
			return nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		rev.ID = 0
		if err := tx.Create(&rev).Error; err != nil {
			return err
		}
		created = true
		return bumpPricingConfigVersion(tx, rev.CreatedAt)
	})
	if err != nil {
		return model.PricingRevision{}, false, translate(err)
	}
	return rev, created, nil
}

// AppendPricingRevision implements store.PricingConfig.
//
// AppendPricingRevision 实现 store.PricingConfig。
func (s *Store) AppendPricingRevision(ctx context.Context, in store.AppendPricingRevision) (model.PricingRevision, error) {
	var rev model.PricingRevision
	err := s.pricingTx(ctx, in.Now, func(tx *gorm.DB) error {
		latest, err := latestPricingRevision(tx)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return store.ErrPricingRevisionChanged
			}
			return err
		}
		if latest.ID != in.BaseRevisionID {
			return store.ErrPricingRevisionChanged
		}
		if len(in.UnusedPlans) > 0 {
			var n int64
			if err := tx.Model(&model.TenantPricePlan{}).
				Where("plan_id IN ? AND valid_to IS NULL", in.UnusedPlans).Count(&n).Error; err != nil {
				return err
			}
			if n > 0 {
				return store.ErrPricePlanInUse
			}
		}
		effective := in.Now
		if effective.Before(latest.EffectiveAt) {
			effective = latest.EffectiveAt
		}
		rev = model.PricingRevision{
			EffectiveAt: effective, Document: in.Document, CreatedBy: in.CreatedBy,
			Summary: in.Summary, CreatedAt: in.Now,
		}
		if err := tx.Create(&rev).Error; err != nil {
			return err
		}
		return bumpPricingConfigVersion(tx, in.Now)
	})
	if err != nil {
		return model.PricingRevision{}, translate(err)
	}
	return rev, nil
}

// SetTenantPricePlan implements store.PricingConfig.
//
// SetTenantPricePlan 实现 store.PricingConfig。
func (s *Store) SetTenantPricePlan(ctx context.Context, in store.SetTenantPricePlan) (bool, error) {
	changed := false
	err := s.pricingTx(ctx, in.Now, func(tx *gorm.DB) error {
		latest, err := latestPricingRevision(tx)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return store.ErrPricingRevisionChanged
			}
			return err
		}
		if latest.ID != in.ExpectRevisionID {
			return store.ErrPricingRevisionChanged
		}
		var open []model.TenantPricePlan
		if err := tx.Where("tenant_id = ? AND valid_to IS NULL", in.TenantID).Find(&open).Error; err != nil {
			return err
		}
		current := ""
		at := in.Now
		for _, row := range open {
			current = row.PlanID
			if at.Before(row.ValidFrom) {
				at = row.ValidFrom
			}
		}
		if current == in.PlanID && len(open) <= 1 {
			return nil
		}
		if len(open) > 0 {
			if err := tx.Model(&model.TenantPricePlan{}).
				Where("tenant_id = ? AND valid_to IS NULL", in.TenantID).Update("valid_to", at).Error; err != nil {
				return err
			}
		}
		if in.PlanID != "" {
			row := model.TenantPricePlan{
				ID: model.NewID(model.PrefixTenantPricePlan), TenantID: in.TenantID, PlanID: in.PlanID,
				ValidFrom: at, CreatedBy: in.CreatedBy,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		changed = true
		return bumpPricingConfigVersion(tx, in.Now)
	})
	if err != nil {
		return false, translate(err)
	}
	return changed, nil
}

// TenantPricePlansAt implements store.PricingConfig.
//
// TenantPricePlansAt 实现 store.PricingConfig。
func (s *Store) TenantPricePlansAt(ctx context.Context, tenantIDs []string, at time.Time) (map[string]string, error) {
	out := make(map[string]string, len(tenantIDs))
	if len(tenantIDs) == 0 {
		return out, nil
	}
	var rows []model.TenantPricePlan
	if err := s.db.WithContext(ctx).
		Where("tenant_id IN ? AND valid_from <= ? AND (valid_to IS NULL OR valid_to > ?)", tenantIDs, at, at).
		Order("valid_from").Find(&rows).Error; err != nil {
		return nil, translate(err)
	}
	for _, row := range rows {
		out[row.TenantID] = row.PlanID
	}
	return out, nil
}

// CountTenantPricePlans implements store.PricingConfig.
//
// CountTenantPricePlans 实现 store.PricingConfig。
func (s *Store) CountTenantPricePlans(ctx context.Context) (map[string]int64, error) {
	var rows []struct {
		PlanID string
		N      int64
	}
	if err := s.db.WithContext(ctx).Model(&model.TenantPricePlan{}).
		Select("plan_id, COUNT(*) AS n").Where("valid_to IS NULL").Group("plan_id").Scan(&rows).Error; err != nil {
		return nil, translate(err)
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[row.PlanID] = row.N
	}
	return out, nil
}

// PricingConfigVersion implements store.PricingConfig.
//
// PricingConfigVersion 实现 store.PricingConfig。
func (s *Store) PricingConfigVersion(ctx context.Context) (int64, error) {
	var state model.PricingConfigState
	err := s.db.WithContext(ctx).Where("id = ?", model.PricingConfigStateID).Take(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	return state.Version, translate(err)
}

// pricingTx runs fn in a READ COMMITTED transaction holding the
// pricing_config_state row lock, so every pricing-configuration write is
// serialized and each read fn makes after the lock sees the writes of the
// transactions that held it before. The row is created first if the
// migration's seed is missing.
//
// pricingTx 在持有 pricing_config_state 行锁的 READ COMMITTED 事务中运行 fn，使每次
// 定价配置写入彼此串行，fn 在拿锁之后的读取都能看到此前持锁事务的写入。迁移种下的那一行
// 缺失时先创建它。
func (s *Store) pricingTx(ctx context.Context, now time.Time, fn func(tx *gorm.DB) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.PricingConfigState{
			ID: model.PricingConfigStateID, UpdatedAt: now,
		}).Error; err != nil {
			return err
		}
		var state model.PricingConfigState
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", model.PricingConfigStateID).Take(&state).Error; err != nil {
			return err
		}
		return fn(tx)
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

// bumpPricingConfigVersion adds one to the version inside a pricingTx.
//
// bumpPricingConfigVersion 在 pricingTx 内让版本号加一。
func bumpPricingConfigVersion(tx *gorm.DB, now time.Time) error {
	return tx.Model(&model.PricingConfigState{}).Where("id = ?", model.PricingConfigStateID).
		Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error
}
