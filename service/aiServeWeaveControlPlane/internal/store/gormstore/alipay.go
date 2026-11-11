package gormstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreateAlipayPurchase implements store.AlipayBilling. A concurrent insert
// of the same action meets the unique index, and the loser reads the
// winner's row.
//
// CreateAlipayPurchase 实现 store.AlipayBilling。同一 action 的并发插入会撞上唯一索引，
// 落败的一方读取获胜方的记录。
func (s *Store) CreateAlipayPurchase(ctx context.Context, row model.AlipayPurchase) (model.AlipayPurchase, error) {
	db := s.db.WithContext(ctx)
	res := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if res.Error != nil {
		return model.AlipayPurchase{}, translate(res.Error)
	}
	if res.RowsAffected == 1 {
		return row, nil
	}
	var existing model.AlipayPurchase
	if err := db.Where("tenant_id = ? AND action_id = ?", row.TenantID, row.ActionID).Take(&existing).Error; err != nil {
		return model.AlipayPurchase{}, translate(err)
	}
	if existing.SkuID != row.SkuID {
		return model.AlipayPurchase{}, store.ErrConflict
	}
	return existing, nil
}

// SaveAlipayPurchase implements store.AlipayBilling.
//
// SaveAlipayPurchase 实现 store.AlipayBilling。
func (s *Store) SaveAlipayPurchase(ctx context.Context, row model.AlipayPurchase) error {
	res := s.db.WithContext(ctx).Model(&model.AlipayPurchase{}).Where("id = ?", row.ID).Updates(map[string]any{
		"status": row.Status, "purchase_url": row.PurchaseURL, "expire_time": row.ExpireTime,
		"reason_code": row.ReasonCode, "updated_at": row.UpdatedAt,
	})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// CreateAlipayRedemption implements store.AlipayBilling. It runs at READ
// COMMITTED under the tenant's billing lock, so the pending check sees every
// redemption committed before that lock.
//
// CreateAlipayRedemption 实现 store.AlipayBilling。以 READ COMMITTED 在租户计费锁下
// 运行，使待确认检查能看到该锁之前提交的全部兑换。
func (s *Store) CreateAlipayRedemption(ctx context.Context, row model.AlipayRedemption) (model.AlipayRedemption, error) {
	var out model.AlipayRedemption
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		out, err = store.CreateAlipayRedemptionRules(gormLedger{tx: tx}, row, row.CreatedAt)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.AlipayRedemption{}, translate(err)
	}
	return out, nil
}

// CreatePresaleAlipayRedemption implements store.AlipayBilling. It runs at
// READ COMMITTED under the tenant's billing lock and then the presale's, so
// the pending and capacity checks see everything committed before them.
//
// CreatePresaleAlipayRedemption 实现 store.AlipayBilling。以 READ COMMITTED 先后在租户计费锁与预售锁下
// 运行，使待确认与名额检查能看到在它们之前提交的全部内容。
func (s *Store) CreatePresaleAlipayRedemption(ctx context.Context, order model.CheckoutOrder, row model.AlipayRedemption) (model.AlipayRedemption, error) {
	var out model.AlipayRedemption
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		out, err = store.CreatePresaleAlipayRules(gormLedger{tx: tx}, order, row, row.CreatedAt)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.AlipayRedemption{}, translate(err)
	}
	return out, nil
}

// SettleAlipayRedemption implements store.AlipayBilling.
//
// SettleAlipayRedemption 实现 store.AlipayBilling。
func (s *Store) SettleAlipayRedemption(ctx context.Context, outcome store.AlipayOutcome, now time.Time) (model.AlipayRedemption, bool, error) {
	var out model.AlipayRedemption
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		out, changed, err = store.SettleAlipayRedemptionRules(gormLedger{tx: tx}, outcome, now)
		return err
	})
	if err != nil {
		return model.AlipayRedemption{}, false, translate(err)
	}
	return out, changed, nil
}

// GetAlipayRedemption implements store.AlipayBilling.
//
// GetAlipayRedemption 实现 store.AlipayBilling。
func (s *Store) GetAlipayRedemption(ctx context.Context, id string) (model.AlipayRedemption, error) {
	var out model.AlipayRedemption
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&out).Error
	return out, translate(err)
}

// ListAlipayRedemptions implements store.AlipayBilling.
//
// ListAlipayRedemptions 实现 store.AlipayBilling。
func (s *Store) ListAlipayRedemptions(ctx context.Context, query store.ListQuery, filter store.AlipayRedemptionFilter) (store.Page[model.AlipayRedemption], error) {
	db := s.db.WithContext(ctx).Model(&model.AlipayRedemption{})
	if filter.TenantID != "" {
		db = db.Where("tenant_id = ?", filter.TenantID)
	}
	if filter.Status != "" {
		db = db.Where("status = ?", filter.Status)
	}
	if !filter.CreatedBefore.IsZero() {
		db = db.Where("created_at < ?", filter.CreatedBefore)
	}
	return readPage(db, query, func(r model.AlipayRedemption) (time.Time, string) { return r.CreatedAt, r.ID })
}

var _ store.AlipayTx = gormLedger{}

func (l gormLedger) AlipayRedemptionByID(id string) (model.AlipayRedemption, error) {
	var out model.AlipayRedemption
	err := l.tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&out).Error
	return out, translate(err)
}

func (l gormLedger) AlipayRedemptionByAction(tenantID, actionID string) (model.AlipayRedemption, error) {
	var out model.AlipayRedemption
	err := l.tx.Where("tenant_id = ? AND action_id = ?", tenantID, actionID).Take(&out).Error
	return out, translate(err)
}

func (l gormLedger) AlipayPendingRedemption(tenantID string) (bool, error) {
	var out model.AlipayRedemption
	err := l.tx.Select("id").Where("tenant_id = ? AND status = ?", tenantID, model.AlipayRedemptionPending).Take(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (l gormLedger) InsertAlipayRedemption(row model.AlipayRedemption) error {
	return l.tx.Create(&row).Error
}

func (l gormLedger) SaveAlipayRedemption(row model.AlipayRedemption) error {
	return l.tx.Model(&model.AlipayRedemption{}).Where("id = ?", row.ID).Updates(map[string]any{
		"status": row.Status, "reason_code": row.ReasonCode, "consume_order_id": row.ConsumeOrderID, "updated_at": row.UpdatedAt,
	}).Error
}

func (l gormLedger) InsertOrder(order model.CheckoutOrder) error {
	return l.tx.Create(&order).Error
}

func (l gormLedger) LockPresale(id string) (model.AccountPresale, error) {
	var out model.AccountPresale
	err := l.tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&out).Error
	return out, translate(err)
}

func (l gormLedger) PresaleOutstanding(presaleID string, now time.Time) (int, error) {
	var n int64
	err := presaleOutstanding(l.tx.Model(&model.CheckoutOrder{}), now).Where("presale_id = ?", presaleID).Count(&n).Error
	return int(n), err
}
