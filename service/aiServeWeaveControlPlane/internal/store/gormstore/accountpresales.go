package gormstore

import (
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"context"
	"database/sql"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// presaleOutstanding narrows orders to those PresaleOrderOutstanding counts.
//
// presaleOutstanding 把订单收窄为 PresaleOrderOutstanding 计入的那些。
func presaleOutstanding(db *gorm.DB, now time.Time) *gorm.DB {
	return db.Where("presale_id <> '' AND ((status = ? AND created_at > ?) OR (status = ? AND account_expires_at IS NULL))",
		model.OrderStatusPending, now.Add(-model.AccountCheckoutHold), model.OrderStatusPaid)
}

// ListAccountPresales implements store.AccountPresales.
//
// ListAccountPresales 实现 store.AccountPresales。
func (s *Store) ListAccountPresales(ctx context.Context, now time.Time) ([]store.PresaleStock, error) {
	db := s.db.WithContext(ctx)
	rows := []model.AccountPresale{}
	if err := db.Order("id").Limit(model.MaxPresales + 1).Find(&rows).Error; err != nil {
		return nil, translate(err)
	}
	var counts []struct {
		PresaleID string
		N         int64
	}
	if err := presaleOutstanding(db.Model(&model.CheckoutOrder{}), now).
		Select("presale_id, COUNT(*) AS n").Group("presale_id").Scan(&counts).Error; err != nil {
		return nil, translate(err)
	}
	byID := make(map[string]int, len(counts))
	for _, c := range counts {
		byID[c.PresaleID] = int(c.N)
	}
	out := make([]store.PresaleStock, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.PresaleStock{AccountPresale: row, Outstanding: byID[row.ID]})
	}
	return out, nil
}

// SaveAccountPresale implements store.AccountPresales. Creators share the
// inventory cap lock, which no payment path takes.
//
// SaveAccountPresale 实现 store.AccountPresales。创建者共用库存容量锁，付款路径
// 不会获取它。
func (s *Store) SaveAccountPresale(ctx context.Context, row model.AccountPresale, create bool) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if create {
			var lock accountSalesLock
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = 1").Take(&lock).Error; err != nil {
				return err
			}
			var count int64
			if err := tx.Model(&model.AccountPresale{}).Count(&count).Error; err != nil {
				return err
			}
			if count >= model.MaxPresales {
				return store.ErrConflict
			}
			return tx.Create(&row).Error
		}
		var old model.AccountPresale
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", row.ID).Take(&old).Error; err != nil {
			return err
		}
		if old.Kind != row.Kind {
			return store.ErrConflict
		}
		return tx.Model(&model.AccountPresale{}).Where("id = ?", row.ID).Updates(map[string]any{
			"name": row.Name, "price_minor": row.PriceMinor, "currency": row.Currency, "dodo_product_id": row.DodoProductID,
			"capacity": row.Capacity, "active": row.Active, "alipay_amount": row.AlipayAmount, "updated_at": row.UpdatedAt,
		}).Error
	}))
}

// CreatePresaleCheckoutOrder implements store.AccountPresales. It runs at
// READ COMMITTED so the count reads committed orders; the presale row lock
// serializes checkouts for the same presale.
//
// CreatePresaleCheckoutOrder 实现 store.AccountPresales。以 READ COMMITTED 运行，使
// 计数读取已提交的订单；预售行锁使同一预售的结账串行。
func (s *Store) CreatePresaleCheckoutOrder(ctx context.Context, order *model.CheckoutOrder, now time.Time) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var presale model.AccountPresale
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", order.PresaleID).Take(&presale).Error; err != nil {
			return err
		}
		var outstanding int64
		if err := presaleOutstanding(tx.Model(&model.CheckoutOrder{}), now).Where("presale_id = ?", order.PresaleID).
			Count(&outstanding).Error; err != nil {
			return err
		}
		if err := store.PresaleCheckoutTerms(presale, order.Provider, order.AmountMinor, order.Currency, int(outstanding)); err != nil {
			return err
		}
		return tx.Create(order).Error
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted}))
}

// AssignPresaleOrder implements store.AccountPresales. It runs at READ
// COMMITTED so the hold check, made under the account lock, sees every
// checkout committed before that lock.
//
// AssignPresaleOrder 实现 store.AccountPresales。以 READ COMMITTED 运行，使在账号锁下
// 进行的占用检查能看到该锁之前提交的全部结账。
func (s *Store) AssignPresaleOrder(ctx context.Context, orderID, accountID string, now time.Time) (model.CheckoutOrder, error) {
	var order model.CheckoutOrder
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		order, err = store.AssignPresaleRules(gormLedger{tx: tx}, orderID, accountID, now)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.CheckoutOrder{}, translate(err)
	}
	return order, nil
}

// GetCheckoutOrder implements store.AccountPresales.
//
// GetCheckoutOrder 实现 store.AccountPresales。
func (s *Store) GetCheckoutOrder(ctx context.Context, id string) (model.CheckoutOrder, error) {
	var out model.CheckoutOrder
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&out).Error
	return out, translate(err)
}

func (l gormLedger) PresaleByID(id string) (model.AccountPresale, error) {
	var out model.AccountPresale
	err := l.tx.Where("id = ?", id).Take(&out).Error
	return out, translate(err)
}

func (l gormLedger) AccountCheckoutHeldByOther(accountID, tenantID string, now time.Time) (bool, error) {
	return accountCheckoutHeld(l.tx, accountID, tenantID, now)
}

// accountCheckoutHeld reports whether another tenant's pending checkout
// holds the account at now.
//
// accountCheckoutHeld 报告在 now 时是否有其他商户的待付款结账占用该账号。
func accountCheckoutHeld(tx *gorm.DB, accountID, tenantID string, now time.Time) (bool, error) {
	var pending []model.CheckoutOrder
	if err := tx.Where("account_id = ? AND status = ? AND created_at > ?", accountID, model.OrderStatusPending, now.Add(-model.AccountCheckoutHold)).
		Find(&pending).Error; err != nil {
		return false, err
	}
	for _, other := range pending {
		if store.AccountCheckoutHeld(other, tenantID, now) {
			return true, nil
		}
	}
	return false, nil
}
